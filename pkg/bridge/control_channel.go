package bridge

import "context"

// ControlChannel abstracts hardware commands away from vendor cloud signaling,
// supporting local MQTT shadow shim control as well as cloud signaling fallbacks.
// HTTP handlers send camera commands only through this interface; vendor
// message names and payloads stay in its implementations.
type ControlChannel interface {
	// MovePTZ pulses the pan/tilt stepper motors (dir: 1=left, 2=right, 3=up, 4=down, 0=stop)
	MovePTZ(ctx context.Context, cameraUUID string, direction int, durationMs int) error

	// SetIR sets infrared illuminator mode (see IRModeAuto/IRModeOn/IRModeOff)
	SetIR(ctx context.Context, cameraUUID string, mode int) error

	// GetIR queries current infrared mode
	GetIR(ctx context.Context, cameraUUID string) (int, error)

	// SetLED controls physical indicator LED (0=off, 1=on)
	SetLED(ctx context.Context, cameraUUID string, on bool) error

	// GetLED queries physical indicator LED status
	GetLED(ctx context.Context, cameraUUID string) (bool, error)

	// SetLight controls physical spotlight on outdoor models (0=off, 1=on)
	SetLight(ctx context.Context, cameraUUID string, on bool) error

	// ArmTalk arms or disarms camera speaker for talkback audio
	ArmTalk(ctx context.Context, cameraUUID string, enable bool, sessionID string) error

	// SetAttribute sets one camera setting by its Osaio attribute name, for
	// switches without a dedicated method (MotionDetectSW, SoundDetectSW,
	// AudioRecordSw).
	SetAttribute(ctx context.Context, cameraUUID, key string, value any) error

	// Close releases resources
	Close() error
}
