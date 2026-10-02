package profile

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestProfile_ZeroLeakage_StringFormatting(t *testing.T) {
	secretPass := "SuperSecretPasswordP@ss999!"
	secretToken := "VendorAuthToken-Secret-XYZ-12345"
	secretKey := "MasterKeyBytesValue32BytesLength"
	refreshToken := "RefreshTokenSecretValue98765"
	camToken := "CameraTokenCacheSecret000"

	now := time.Now().UTC()
	p := &Profile{
		Version:   1,
		CreatedAt: now,
		UpdatedAt: now,
		Privacy:   PrivacySettings{BlockCloudVideo: BoolPtr(true)},
		Accounts: []CloudCredentials{{
			AccountEmail:   "user@example.com",
			Password:       SecretString(secretPass),
			AuthToken:      SecretString(secretToken),
			RefreshToken:   SecretString(refreshToken),
			TokenExpiresAt: now.Add(24 * time.Hour),
			VendorUID:      "uid-test-123",
			Region:         "us-east-1",
		}},
		Cameras: map[string]CameraProfile{
			"cam-uuid-1": {
				UUID:       "cam-uuid-1",
				Name:       "Front Yard",
				IPAddress:  "192.168.1.50",
				TokenCache: SecretString(camToken),
			},
		},
	}

	formats := []struct {
		name   string
		format string
		val    any
	}{
		{"Profile_v", "%v", p},
		{"Profile_val_v", "%v", *p},
		{"Profile_plus_v", "%+v", p},
		{"Profile_val_plus_v", "%+v", *p},
		{"Profile_hash_v", "%#v", p},
		{"Profile_val_hash_v", "%#v", *p},
		{"Profile_s", "%s", p},
		{"Profile_val_s", "%s", *p},
		{"Creds_v", "%v", p.Accounts[0]},
		{"Creds_plus_v", "%+v", p.Accounts[0]},
		{"Creds_hash_v", "%#v", p.Accounts[0]},
		{"SecretString_s", "%s", p.Accounts[0].Password},
		{"SecretString_v", "%v", p.Accounts[0].Password},
		{"SecretString_plus_v", "%+v", p.Accounts[0].Password},
		{"SecretString_hash_v", "%#v", p.Accounts[0].Password},
		{"SecretString_q", "%q", p.Accounts[0].Password},
		{"SecretBytes_v", "%v", SecretBytes(secretKey)},
		{"SecretBytes_plus_v", "%+v", SecretBytes(secretKey)},
		{"SecretBytes_hash_v", "%#v", SecretBytes(secretKey)},
	}

	forbidden := []string{secretPass, secretToken, secretKey, refreshToken, camToken}

	for _, tc := range formats {
		t.Run(tc.name, func(t *testing.T) {
			output := fmt.Sprintf(tc.format, tc.val)
			for _, leak := range forbidden {
				if strings.Contains(output, leak) {
					t.Fatalf("[%s] LEAK DETECTED: output contained %q. Output: %s", tc.name, leak, output)
				}
			}
			if !strings.Contains(output, RedactedPlaceholder) {
				t.Fatalf("[%s] Expected %q in output, got: %s", tc.name, RedactedPlaceholder, output)
			}
		})
	}
}

func TestProfile_ZeroLeakage_JSONSerialization(t *testing.T) {
	secretPass := "SuperSecretPasswordP@ss999!"
	secretToken := "VendorAuthToken-Secret-XYZ-12345"
	camToken := "CameraSecretToken000"

	p := &Profile{
		Version: 1,
		Privacy: PrivacySettings{BlockCloudVideo: BoolPtr(true)},
		Accounts: []CloudCredentials{{
			AccountEmail: "user@example.com",
			Password:     SecretString(secretPass),
			AuthToken:    SecretString(secretToken),
			VendorUID:    "uid-test-123",
		}},
		Cameras: map[string]CameraProfile{
			"cam-uuid-1": {
				UUID:       "cam-uuid-1",
				Name:       "Driveway",
				TokenCache: SecretString(camToken),
			},
		},
	}

	cases := []struct {
		name string
		val  any
	}{
		{"ProfilePtr", p},
		{"ProfileVal", *p},
		{"CredentialsPtr", &p.Accounts[0]},
		{"CredentialsVal", p.Accounts[0]},
		{"PublicView", p.RedactSecrets()},
		{"SecretString", p.Accounts[0].Password},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, err := json.Marshal(tc.val)
			if err != nil {
				t.Fatalf("[%s] json.Marshal failed: %v", tc.name, err)
			}
			jsonStr := string(b)

			if strings.Contains(jsonStr, secretPass) {
				t.Fatalf("[%s] LEAK DETECTED: json contained plaintext password: %s", tc.name, jsonStr)
			}
			if strings.Contains(jsonStr, secretToken) {
				t.Fatalf("[%s] LEAK DETECTED: json contained plaintext auth token: %s", tc.name, jsonStr)
			}
			if strings.Contains(jsonStr, camToken) {
				t.Fatalf("[%s] LEAK DETECTED: json contained camera token: %s", tc.name, jsonStr)
			}
		})
	}

	// Verify capability indicators in public view
	viewJSON, err := json.Marshal(p.RedactSecrets())
	if err != nil {
		t.Fatalf("json.Marshal(RedactSecrets) failed: %v", err)
	}
	viewStr := string(viewJSON)
	if !strings.Contains(viewStr, `"has_password":true`) {
		t.Fatalf("expected has_password:true in %s", viewStr)
	}
	if !strings.Contains(viewStr, `"has_token":true`) {
		t.Fatalf("expected has_token:true in %s", viewStr)
	}
}

func TestProfile_ZeroLeakage_StructuredLogging(t *testing.T) {
	secretPass := "SuperSecretPasswordP@ss999!"
	secretToken := "VendorAuthToken-Secret-XYZ-12345"

	p := &Profile{
		Version: 1,
		Privacy: PrivacySettings{BlockCloudVideo: BoolPtr(true)},
		Accounts: []CloudCredentials{{
			AccountEmail: "user@example.com",
			Password:     SecretString(secretPass),
			AuthToken:    SecretString(secretToken),
			VendorUID:    "uid-test-123",
		}},
	}

	// Text handler
	textBuf := &bytes.Buffer{}
	textLogger := slog.New(slog.NewTextHandler(textBuf, nil))
	textLogger.Info("profile event", "profile", p)

	if strings.Contains(textBuf.String(), secretPass) || strings.Contains(textBuf.String(), secretToken) {
		t.Fatalf("LEAK DETECTED in slog TextHandler: %s", textBuf.String())
	}
	if !strings.Contains(textBuf.String(), RedactedPlaceholder) {
		t.Fatalf("Expected %s in slog TextHandler output: %s", RedactedPlaceholder, textBuf.String())
	}

	// JSON handler
	jsonBuf := &bytes.Buffer{}
	jsonLogger := slog.New(slog.NewJSONHandler(jsonBuf, nil))
	jsonLogger.Info("profile event", "profile", p)

	if strings.Contains(jsonBuf.String(), secretPass) || strings.Contains(jsonBuf.String(), secretToken) {
		t.Fatalf("LEAK DETECTED in slog JSONHandler: %s", jsonBuf.String())
	}
	if !strings.Contains(jsonBuf.String(), RedactedPlaceholder) {
		t.Fatalf("Expected %s in slog JSONHandler output: %s", RedactedPlaceholder, jsonBuf.String())
	}
}

func TestSecretBytes_Destroy(t *testing.T) {
	raw := []byte("SensitiveRawSecretData1234567890")
	sb := SecretBytes(raw)

	if sb.String() != RedactedPlaceholder {
		t.Fatalf("expected String() to return %q, got %q", RedactedPlaceholder, sb.String())
	}

	exposed := sb.Expose()
	if string(exposed) != "SensitiveRawSecretData1234567890" {
		t.Fatalf("unexpected Expose() value: %s", string(exposed))
	}

	sb.Destroy()
	for i, b := range sb {
		if b != 0 {
			t.Fatalf("byte at index %d not zeroed: %d", i, b)
		}
	}
}

func TestSecretString_Equal(t *testing.T) {
	s1 := SecretString("my-secret")
	s2 := SecretString("my-secret")
	s3 := SecretString("other-secret")

	if !s1.Equal(s2) {
		t.Fatalf("expected s1 == s2")
	}
	if s1.Equal(s3) {
		t.Fatalf("expected s1 != s3")
	}
}

func TestInternalProfileDisk_RoundTrip(t *testing.T) {
	p := &Profile{
		Version: 1,
		Privacy: PrivacySettings{BlockCloudVideo: BoolPtr(true)},
		Accounts: []CloudCredentials{{
			AccountEmail: "user@example.com",
			Password:     SecretString("SecretPass123!"),
		}},
	}

	encoded, err := encodeInternal(p)
	if err != nil {
		t.Fatalf("encodeInternal failed: %v", err)
	}

	decoded, err := decodeInternal(encoded)
	if err != nil {
		t.Fatalf("decodeInternal failed: %v", err)
	}

	if len(decoded.Accounts) != 1 || decoded.Accounts[0].AccountEmail != p.Accounts[0].AccountEmail {
		t.Fatalf("expected login %q, got %+v", p.Accounts[0].AccountEmail, decoded.Accounts)
	}
	if decoded.Accounts[0].Password.Expose() != "SecretPass123!" {
		t.Fatalf("expected password preserved, got %q", decoded.Accounts[0].Password.Expose())
	}
	if b := decoded.Privacy.BlockCloudVideo; b == nil || !*b {
		t.Fatalf("expected block cloud video true")
	}
}

// Older profiles carry a "privacy_mode" flag. It
// must load cleanly and must not be read as a Block cloud video choice: the
// router state is what counts, and it has never been applied.
func TestDecodeLegacyPrivacyModeProfile(t *testing.T) {
	legacy := []byte(`{"version":1,"privacy_mode":true,"credentials":{"account_email":"a@example.com"},"cameras":{}}`)
	p, err := decodeInternal(legacy)
	if err != nil {
		t.Fatalf("legacy profile rejected: %v", err)
	}
	if p.Privacy.BlockCloudVideo != nil {
		t.Fatalf("legacy privacy_mode must not become a Block cloud video setting: %+v", p.Privacy)
	}
}

// BombeCam's router key lives only in the encrypted profile: it round-trips
// through the disk format and never appears in JSON, String or logs.
func TestRouterKeyStaysSecret(t *testing.T) {
	const key = "-----BEGIN OPENSSH PRIVATE KEY-----\nROUTERKEYMATERIAL\n-----END OPENSSH PRIVATE KEY-----\n"
	p := &Profile{Version: CurrentSchemaVersion, RouterKey: SecretString(key), Cameras: map[string]CameraProfile{},
		Privacy: PrivacySettings{Blocked: map[string]bool{"cam-1": true}, BlockNewCameras: true, RouterConnected: true}}
	b, err := encodeInternal(p)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodeInternal(b)
	if err != nil {
		t.Fatal(err)
	}
	if got.RouterKey.Expose() != key || !got.Privacy.Blocked["cam-1"] || !got.Privacy.BlockNewCameras || !got.Privacy.RouterConnected {
		t.Fatalf("round trip lost data: %+v", got.Privacy)
	}
	if c := got.Clone(); c.RouterKey.Expose() != key {
		t.Fatal("Clone dropped the router key")
	}
	pub, _ := json.Marshal(p)
	direct, _ := json.Marshal(p.Privacy)
	for _, s := range []string{string(pub), string(direct), p.String(), fmt.Sprintf("%+v %#v", p, p)} {
		if strings.Contains(s, "ROUTERKEYMATERIAL") {
			t.Fatalf("router key leaked: %s", s)
		}
	}
	// Clone must not share the Blocked map.
	c := p.Clone()
	c.Privacy.Blocked["cam-1"] = false
	if !p.Privacy.Blocked["cam-1"] {
		t.Fatal("Clone shares the Blocked map")
	}
}

func TestIntegrationSecretsStaySecret(t *testing.T) {
	const streamPass, mqttPass = "STREAMPASSWORD123", "MQTTPASSWORD456"
	p := &Profile{Version: CurrentSchemaVersion, Cameras: map[string]CameraProfile{},
		Integrations: IntegrationSettings{
			NVRAddress: "10.0.0.5", StreamAuth: true, StreamUser: "bombecam", StreamPassword: SecretString(streamPass),
			Snapshots: true, RTSPPort: 9554,
			MQTT: MQTTSettings{Enabled: true, Host: "10.0.0.2", Port: 1883, Username: "ha", Password: SecretString(mqttPass), DiscoveryPrefix: "homeassistant"},
		}}
	b, err := encodeInternal(p)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodeInternal(b)
	if err != nil {
		t.Fatal(err)
	}
	gi := got.Integrations
	if gi.StreamPassword.Expose() != streamPass || gi.MQTT.Password.Expose() != mqttPass || gi.NVRAddress != "10.0.0.5" ||
		!gi.StreamAuth || gi.StreamUser != "bombecam" || !gi.Snapshots || gi.RTSPPort != 9554 || gi.MQTT.Host != "10.0.0.2" || !gi.MQTT.Enabled {
		t.Fatalf("round trip lost data: %+v", gi)
	}
	if c := got.Clone(); c.Integrations.MQTT.Password.Expose() != mqttPass {
		t.Fatal("Clone dropped the MQTT password")
	}
	pub, _ := json.Marshal(p)
	direct, _ := json.Marshal(p.Integrations)
	view, _ := json.Marshal(p.RedactSecrets())
	for _, s := range []string{string(pub), string(direct), string(view), p.String(), fmt.Sprintf("%+v %#v %v", p, p, p.Integrations)} {
		if strings.Contains(s, streamPass) || strings.Contains(s, mqttPass) {
			t.Fatalf("integration secret leaked: %s", s)
		}
	}
	if !strings.Contains(string(view), `"nvr_address":"10.0.0.5"`) {
		t.Errorf("public view lacks the integration settings: %s", view)
	}
	// older profiles have no integrations block
	old, err := decodeInternal([]byte(`{"version":1,"cameras":{}}`))
	if err != nil || old.Integrations.NVRAddress != "" || old.Integrations.StreamAuth {
		t.Fatalf("old profile: %+v %v", old.Integrations, err)
	}
}
