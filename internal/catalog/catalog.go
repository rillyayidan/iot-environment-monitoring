package catalog

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

var (
	ErrNotFound     = errors.New("resource not found")
	ErrInvalid      = errors.New("invalid request")
	ErrUnauthorized = errors.New("invalid device credentials")
)

type Device struct {
	ID       uuid.UUID  `json:"id"`
	Name     string     `json:"name"`
	Location string     `json:"location"`
	Active   bool       `json:"is_active"`
	LastSeen *time.Time `json:"last_seen_at"`
	Created  time.Time  `json:"created_at"`
}

type DeviceInput struct {
	Name     *string `json:"name"`
	Location *string `json:"location"`
	Active   *bool   `json:"is_active"`
}

type DeviceCredential struct {
	Device
	APIKey string `json:"api_key"`
}

type Sensor struct {
	ID       uuid.UUID `json:"id"`
	DeviceID uuid.UUID `json:"device_id"`
	Metric   string    `json:"metric"`
	Unit     string    `json:"unit"`
	Active   bool      `json:"is_active"`
	Created  time.Time `json:"created_at"`
}

type SensorInput struct {
	Metric string `json:"metric"`
	Active *bool  `json:"is_active"`
}

type SensorPatch struct {
	Active *bool `json:"is_active"`
}

func Unit(metric string) (string, bool) {
	switch metric {
	case "co2":
		return "ppm", true
	case "temperature":
		return "celsius", true
	case "humidity":
		return "percent", true
	default:
		return "", false
	}
}

type Repository interface {
	CreateDevice(context.Context, DeviceInput, string) (Device, error)
	ListDevices(context.Context, int, int) ([]Device, error)
	Device(context.Context, uuid.UUID) (Device, error)
	PatchDevice(context.Context, uuid.UUID, DeviceInput) (Device, error)
	RotateDeviceID(context.Context, uuid.UUID) (Device, error)
	RotateKey(context.Context, uuid.UUID, string) (Device, error)
	CreateSensor(context.Context, uuid.UUID, SensorInput, string) (Sensor, error)
	ListSensors(context.Context, uuid.UUID, int, int) ([]Sensor, error)
	Sensor(context.Context, uuid.UUID) (Sensor, error)
	PatchSensor(context.Context, uuid.UUID, bool) (Sensor, error)
	RotateSensorID(context.Context, uuid.UUID) (Sensor, error)
	Authenticate(context.Context, string) (uuid.UUID, error)
}

type Service struct {
	repo Repository
}

func New(repo Repository) *Service {
	return &Service{repo: repo}
}

func cleanText(value *string, max int, field string) (*string, error) {
	if value == nil {
		return nil, nil
	}
	clean := strings.TrimSpace(*value)
	if clean == "" || utf8.RuneCountInString(clean) > max || strings.ContainsRune(clean, 0) {
		return nil, fmt.Errorf("%w: %s must have 1 to %d characters", ErrInvalid, field, max)
	}
	return &clean, nil
}

func validateDevice(input DeviceInput) (DeviceInput, error) {
	name, err := cleanText(input.Name, 100, "name")
	if err != nil {
		return DeviceInput{}, err
	}
	location, err := cleanText(input.Location, 200, "location")
	if err != nil {
		return DeviceInput{}, err
	}
	return DeviceInput{Name: name, Location: location, Active: input.Active}, nil
}

func hashKey(key string) string {
	digest := sha256.Sum256([]byte(key))
	return hex.EncodeToString(digest[:])
}

func newKey() string {
	var entropy [32]byte
	rand.Read(entropy[:])
	return base64.RawURLEncoding.EncodeToString(entropy[:])
}

func (s *Service) RegisterDevice(ctx context.Context, input DeviceInput) (DeviceCredential, error) {
	if input.Name == nil || input.Location == nil {
		return DeviceCredential{}, fmt.Errorf("%w: name and location are required", ErrInvalid)
	}
	input, err := validateDevice(input)
	if err != nil {
		return DeviceCredential{}, err
	}
	key := newKey()
	device, err := s.repo.CreateDevice(ctx, input, hashKey(key))
	if err != nil {
		return DeviceCredential{}, err
	}
	return DeviceCredential{Device: device, APIKey: key}, nil
}

func (s *Service) ListDevices(ctx context.Context, limit, offset int) ([]Device, error) {
	return s.repo.ListDevices(ctx, limit, offset)
}

func (s *Service) Device(ctx context.Context, id uuid.UUID) (Device, error) {
	return s.repo.Device(ctx, id)
}

func (s *Service) PatchDevice(ctx context.Context, id uuid.UUID, input DeviceInput) (Device, error) {
	if input.Name == nil && input.Location == nil && input.Active == nil {
		return Device{}, fmt.Errorf("%w: at least one device field is required", ErrInvalid)
	}
	input, err := validateDevice(input)
	if err != nil {
		return Device{}, err
	}
	return s.repo.PatchDevice(ctx, id, input)
}

func (s *Service) RotateDeviceID(ctx context.Context, id uuid.UUID) (Device, error) {
	return s.repo.RotateDeviceID(ctx, id)
}

func (s *Service) RotateKey(ctx context.Context, id uuid.UUID) (DeviceCredential, error) {
	key := newKey()
	device, err := s.repo.RotateKey(ctx, id, hashKey(key))
	if err != nil {
		return DeviceCredential{}, err
	}
	return DeviceCredential{Device: device, APIKey: key}, nil
}

func (s *Service) RegisterSensor(ctx context.Context, device uuid.UUID, input SensorInput) (Sensor, error) {
	unit, ok := Unit(input.Metric)
	if !ok {
		return Sensor{}, fmt.Errorf("%w: unsupported sensor metric", ErrInvalid)
	}
	return s.repo.CreateSensor(ctx, device, input, unit)
}

func (s *Service) ListSensors(ctx context.Context, device uuid.UUID, limit, offset int) ([]Sensor, error) {
	return s.repo.ListSensors(ctx, device, limit, offset)
}

func (s *Service) Sensor(ctx context.Context, id uuid.UUID) (Sensor, error) {
	return s.repo.Sensor(ctx, id)
}

func (s *Service) PatchSensor(ctx context.Context, id uuid.UUID, input SensorPatch) (Sensor, error) {
	if input.Active == nil {
		return Sensor{}, fmt.Errorf("%w: is_active is required", ErrInvalid)
	}
	return s.repo.PatchSensor(ctx, id, *input.Active)
}

func (s *Service) RotateSensorID(ctx context.Context, id uuid.UUID) (Sensor, error) {
	return s.repo.RotateSensorID(ctx, id)
}

func (s *Service) Authenticate(ctx context.Context, key string) (uuid.UUID, error) {
	if len(key) != base64.RawURLEncoding.EncodedLen(32) {
		return uuid.Nil, ErrUnauthorized
	}
	if _, err := base64.RawURLEncoding.Strict().DecodeString(key); err != nil {
		return uuid.Nil, ErrUnauthorized
	}
	return s.repo.Authenticate(ctx, hashKey(key))
}
