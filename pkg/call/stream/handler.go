// pkg/call/stream/handler.go
package call_stream

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"

	call_service "github.com/evolution-foundation/evolution-go/pkg/call/service"
	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	instance_service "github.com/evolution-foundation/evolution-go/pkg/instance/service"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/purpshell/meowcaller"
)

const (
	streamInstanceContextKey = "callStreamInstance"
	streamSigningKeyEnv      = "CALL_STREAM_SIGNING_KEY"
	streamClockSkewSeconds   = int64(5)
)

var errStreamUnauthorized = errors.New("not authorized")

type streamInstanceResolver interface {
	GetInstanceByToken(token string) (*instance_model.Instance, error)
	Info(instanceID string) (*instance_model.Instance, error)
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

// RegisterRoutes mounts GET /call/stream/:callId directly on the engine, bypassing the
// header-based authMiddleware chain used by pkg/routes. It accepts the legacy apikey
// query parameter or a short-lived, per-call HMAC suitable for browser clients.
func RegisterRoutes(r *gin.Engine, callService call_service.CallService, instanceService instance_service.InstanceService) {
	r.GET("/call/stream/:callId", streamAuthMiddleware(instanceService, time.Now), serveStream(callService))
}

func streamAuthMiddleware(instanceService streamInstanceResolver, now func() time.Time) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		instance, err := authenticateStream(ctx, instanceService, now())
		if err != nil {
			ctx.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "not authorized"})
			return
		}
		ctx.Set(streamInstanceContextKey, instance)
		ctx.Next()
	}
}

func authenticateStream(ctx *gin.Context, instanceService streamInstanceResolver, now time.Time) (*instance_model.Instance, error) {
	if apiKey := ctx.Query("apikey"); apiKey != "" {
		return instanceService.GetInstanceByToken(apiKey)
	}

	instanceID := ctx.Query("instance")
	exp, err := strconv.ParseInt(ctx.Query("exp"), 10, 64)
	if err != nil || exp < now.Unix()-streamClockSkewSeconds {
		return nil, errStreamUnauthorized
	}

	key := os.Getenv(streamSigningKeyEnv)
	if key == "" {
		return nil, errStreamUnauthorized
	}

	message := fmt.Sprintf("%s|%s|%d", instanceID, ctx.Param("callId"), exp)
	mac := hmac.New(sha256.New, []byte(key))
	_, _ = mac.Write([]byte(message))
	expected := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(ctx.Query("token"))) {
		return nil, errStreamUnauthorized
	}

	return instanceService.Info(instanceID)
}

func serveStream(callService call_service.CallService) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		instanceValue, ok := ctx.Get(streamInstanceContextKey)
		instance, valid := instanceValue.(*instance_model.Instance)
		if !ok || !valid {
			ctx.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "not authorized"})
			return
		}

		callID := ctx.Param("callId")
		call, err := callService.GetActiveCall(instance.Id, callID)
		if err != nil {
			ctx.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": err.Error()})
			return
		}

		conn, err := upgrader.Upgrade(ctx.Writer, ctx.Request, nil)
		if err != nil {
			return
		}

		b := newBridge(conn)
		if callService.IsOutgoingCall(instance.Id, callID) {
			// OnPeerAccept replays immediately if acceptance preceded registration.
			// Do not use CallPhaseActive here: meowcaller may start media before the
			// remote peer accepts an outbound call.
			call.OnPeerAccept(b.allowInbound)
		} else {
			call.OnStateChange(func(phase meowcaller.CallPhase) {
				if phase == meowcaller.CallPhaseActive {
					b.allowInbound()
				}
			})
			// Check after registering the callback so an incoming call that became
			// active during setup cannot miss the state-change notification.
			if call.State() == meowcaller.CallPhaseActive {
				b.allowInbound()
			}
		}
		b.sendVideo = call.SendVideo
		// StartVideo requests an audio->video upgrade. Calling it on a call that
		// already has video (declared in the original offer) doesn't just no-op --
		// live testing showed it can drop the call entirely. Only wire it up when
		// there's an actual upgrade to request.
		if !call.IsVideo() {
			b.startVideo = call.StartVideo
		}
		call.OnEnd(func(reason string) { b.Close() })
		call.Receive(b)
		call.ReceiveVideo(b)
		call.Play(b)

		b.writeStart(callID, call.IsVideo())
		b.readLoop() // blocks until the socket closes, from either end

		_ = call.Hangup()
	}
}
