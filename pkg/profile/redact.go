package profile

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"
)

// RedactedPlaceholder is the universal masking string for sensitive secrets.
const RedactedPlaceholder = "[REDACTED]"

// SecretString encapsulates sensitive string values such as passwords, tokens, and passphrases.
type SecretString string

// String ensures fmt.Printf (%s, %v, %+v) prints [REDACTED].
func (s SecretString) String() string {
	return RedactedPlaceholder
}

// GoString ensures fmt.Printf (%#v) prints "[REDACTED]".
func (s SecretString) GoString() string {
	return fmt.Sprintf("%q", RedactedPlaceholder)
}

// LogValue implements slog.LogValuer for structured logging.
func (s SecretString) LogValue() slog.Value {
	return slog.StringValue(RedactedPlaceholder)
}

// MarshalJSON guarantees any direct JSON marshaling of SecretString outputs "[REDACTED]".
func (s SecretString) MarshalJSON() ([]byte, error) {
	return json.Marshal(RedactedPlaceholder)
}

// Expose returns the underlying plaintext secret.
// Must only be called at authenticated cryptographic or vendor API boundaries.
func (s SecretString) Expose() string {
	return string(s)
}

// IsEmpty returns true if the secret string has zero length.
func (s SecretString) IsEmpty() bool {
	return len(s) == 0
}

// Equal performs a constant-time comparison to prevent timing side channels.
func (s SecretString) Equal(other SecretString) bool {
	return subtle.ConstantTimeCompare([]byte(s), []byte(other)) == 1
}

// SecretBytes encapsulates sensitive byte slices such as raw cryptographic keys.
type SecretBytes []byte

// String ensures fmt.Printf prints [REDACTED].
func (b SecretBytes) String() string {
	return RedactedPlaceholder
}

// GoString ensures fmt.Printf (%#v) prints "[REDACTED]".
func (b SecretBytes) GoString() string {
	return fmt.Sprintf("%q", RedactedPlaceholder)
}

// LogValue implements slog.LogValuer for structured logging.
func (b SecretBytes) LogValue() slog.Value {
	return slog.StringValue(RedactedPlaceholder)
}

// MarshalJSON guarantees any direct JSON marshaling outputs "[REDACTED]".
func (b SecretBytes) MarshalJSON() ([]byte, error) {
	return json.Marshal(RedactedPlaceholder)
}

// Expose returns a copy of the underlying byte slice.
func (b SecretBytes) Expose() []byte {
	if b == nil {
		return nil
	}
	cp := make([]byte, len(b))
	copy(cp, b)
	return cp
}

// Destroy zeroes out sensitive memory bytes.
func (b SecretBytes) Destroy() {
	for i := range b {
		b[i] = 0
	}
}

// internalProfileDisk is unexported and used exclusively for AES-GCM encrypted persistence.
type internalProfileDisk struct {
	Version   int             `json:"version"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
	Privacy   PrivacySettings `json:"privacy"`
	// Credentials is the single login of profiles from older versions. It is
	// read so Normalize can move it into Accounts, and never written.
	Credentials  *internalCredsDisk         `json:"credentials,omitempty"`
	Accounts     []internalCredsDisk        `json:"accounts,omitempty"`
	Cameras      map[string]internalCamDisk `json:"cameras"`
	Connection   ConnectionParameters       `json:"connection"`
	RouterKey    string                     `json:"router_key,omitempty"`
	Integrations internalIntegrationsDisk   `json:"integrations"`
	Users        []internalUserDisk         `json:"users,omitempty"`
	Security     SecuritySettings           `json:"security"`
}

// internalUserDisk is UserProfile with its secrets exposed, for the
// encrypted file only.
type internalUserDisk struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Role          string    `json:"role"`
	PasswordHash  string    `json:"password_hash"`
	TOTPSecret    string    `json:"totp_secret,omitempty"`
	TOTPLastStep  int64     `json:"totp_last_step,omitempty"`
	RecoveryCodes []string  `json:"recovery_codes,omitempty"`
	PasswordHint  string    `json:"password_hint,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

// internalIntegrationsDisk is IntegrationSettings with its secrets exposed,
// for the encrypted file only.
type internalIntegrationsDisk struct {
	Settings       IntegrationSettings `json:"settings"`
	StreamPassword string              `json:"stream_password,omitempty"`
	MQTTPassword   string              `json:"mqtt_password,omitempty"`
}

func integrationsToDisk(s IntegrationSettings) internalIntegrationsDisk {
	return internalIntegrationsDisk{Settings: s, StreamPassword: s.StreamPassword.Expose(), MQTTPassword: s.MQTT.Password.Expose()}
}

func diskToIntegrations(d internalIntegrationsDisk) IntegrationSettings {
	s := d.Settings
	s.StreamPassword = SecretString(d.StreamPassword)
	s.MQTT.Password = SecretString(d.MQTTPassword)
	return s
}

type internalCredsDisk struct {
	AccountEmail   string    `json:"account_email"`
	Password       string    `json:"password,omitempty"`
	Country        string    `json:"country,omitempty"`
	PhoneCode      string    `json:"phone_code,omitempty"`
	VendorUID      string    `json:"vendor_uid,omitempty"`
	AuthToken      string    `json:"auth_token,omitempty"`
	RefreshToken   string    `json:"refresh_token,omitempty"`
	TokenExpiresAt time.Time `json:"token_expires_at,omitempty"`
	Region         string    `json:"region,omitempty"`
}

type internalCamDisk struct {
	UUID         string             `json:"uuid"`
	AccountEmail string             `json:"account_email,omitempty"`
	Name         string             `json:"name"`
	Model        string             `json:"model"`
	IPAddress    string             `json:"ip_address,omitempty"`
	MACAddress   string             `json:"mac_address,omitempty"`
	EnrolledAt   time.Time          `json:"enrolled_at"`
	LastSeenAt   time.Time          `json:"last_seen_at,omitempty"`
	Online       bool               `json:"online"`
	TokenCache   string             `json:"token_cache,omitempty"`
	StreamConfig CameraStreamConfig `json:"stream_config,omitempty"`
}

// credsToDisk converts CloudCredentials to its on-disk (secret-exposing) form.
func credsToDisk(c CloudCredentials) internalCredsDisk {
	return internalCredsDisk{
		AccountEmail:   c.AccountEmail,
		Password:       c.Password.Expose(),
		Country:        c.Country,
		PhoneCode:      c.PhoneCode,
		VendorUID:      c.VendorUID,
		AuthToken:      c.AuthToken.Expose(),
		RefreshToken:   c.RefreshToken.Expose(),
		TokenExpiresAt: c.TokenExpiresAt,
		Region:         c.Region,
	}
}

// diskToCreds is the inverse of credsToDisk.
func diskToCreds(d internalCredsDisk) CloudCredentials {
	return CloudCredentials{
		AccountEmail:   d.AccountEmail,
		Password:       SecretString(d.Password),
		Country:        d.Country,
		PhoneCode:      d.PhoneCode,
		VendorUID:      d.VendorUID,
		AuthToken:      SecretString(d.AuthToken),
		RefreshToken:   SecretString(d.RefreshToken),
		TokenExpiresAt: d.TokenExpiresAt,
		Region:         d.Region,
	}
}

// encodeInternal serializes a Profile into JSON plaintext intended for AES-256-GCM encryption.
func encodeInternal(p *Profile) ([]byte, error) {
	if p == nil {
		return nil, ErrCorruptedProfile
	}
	if p.Credentials.AccountEmail != "" {
		// Never drop an older profile's login: move it into Accounts first.
		p = p.Clone()
		p.Normalize()
	}
	disk := internalProfileDisk{
		Version:   p.Version,
		CreatedAt: p.CreatedAt,
		UpdatedAt: p.UpdatedAt,
		Privacy:   p.Privacy.Clone(),
		Accounts: func() []internalCredsDisk {
			out := make([]internalCredsDisk, 0, len(p.Accounts))
			for _, a := range p.Accounts {
				out = append(out, credsToDisk(a))
			}
			return out
		}(),
		Cameras:      make(map[string]internalCamDisk, len(p.Cameras)),
		Connection:   p.Connection,
		RouterKey:    p.RouterKey.Expose(),
		Integrations: integrationsToDisk(p.Integrations),
		Security:     p.Security,
	}
	for _, u := range p.Users {
		disk.Users = append(disk.Users, internalUserDisk{ID: u.ID, Name: u.Name, Role: u.Role,
			PasswordHash: u.PasswordHash.Expose(), TOTPSecret: u.TOTPSecret.Expose(), TOTPLastStep: u.TOTPLastStep,
			RecoveryCodes: u.RecoveryCodes, PasswordHint: u.PasswordHint, CreatedAt: u.CreatedAt})
	}

	for k, c := range p.Cameras {
		disk.Cameras[k] = internalCamDisk{
			UUID:         c.UUID,
			AccountEmail: c.AccountEmail,
			Name:         c.Name,
			Model:        c.Model,
			IPAddress:    c.IPAddress,
			MACAddress:   c.MACAddress,
			EnrolledAt:   c.EnrolledAt,
			LastSeenAt:   c.LastSeenAt,
			Online:       c.Online,
			TokenCache:   c.TokenCache.Expose(),
			StreamConfig: c.StreamConfig,
		}
	}

	return json.Marshal(disk)
}

// decodeInternal deserializes unencrypted JSON plaintext from AES-256-GCM decryption into Profile.
func decodeInternal(b []byte) (*Profile, error) {
	var disk internalProfileDisk
	if err := json.Unmarshal(b, &disk); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCorruptedProfile, err)
	}

	p := &Profile{
		Version:   disk.Version,
		CreatedAt: disk.CreatedAt,
		UpdatedAt: disk.UpdatedAt,
		Privacy:   disk.Privacy.Clone(),
		Accounts: func() []CloudCredentials {
			out := make([]CloudCredentials, 0, len(disk.Accounts))
			for _, a := range disk.Accounts {
				out = append(out, diskToCreds(a))
			}
			return out
		}(),
		Cameras:      make(map[string]CameraProfile, len(disk.Cameras)),
		Connection:   disk.Connection,
		RouterKey:    SecretString(disk.RouterKey),
		Integrations: diskToIntegrations(disk.Integrations),
	}

	if disk.Credentials != nil {
		p.Credentials = diskToCreds(*disk.Credentials)
	}
	p.Security = disk.Security
	for _, u := range disk.Users {
		p.Users = append(p.Users, UserProfile{ID: u.ID, Name: u.Name, Role: u.Role,
			PasswordHash: SecretString(u.PasswordHash), TOTPSecret: SecretString(u.TOTPSecret), TOTPLastStep: u.TOTPLastStep,
			RecoveryCodes: u.RecoveryCodes, PasswordHint: u.PasswordHint, CreatedAt: u.CreatedAt})
	}
	for k, c := range disk.Cameras {
		p.Cameras[k] = CameraProfile{
			UUID:         c.UUID,
			AccountEmail: c.AccountEmail,
			Name:         c.Name,
			Model:        c.Model,
			IPAddress:    c.IPAddress,
			MACAddress:   c.MACAddress,
			EnrolledAt:   c.EnrolledAt,
			LastSeenAt:   c.LastSeenAt,
			Online:       c.Online,
			TokenCache:   SecretString(c.TokenCache),
			StreamConfig: c.StreamConfig,
		}
	}

	p.Normalize()
	return p, nil
}
