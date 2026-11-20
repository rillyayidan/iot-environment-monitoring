package ingest

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestValidValue(t *testing.T) {
	for _, test := range []struct {
		name   string
		metric string
		value  float64
		want   bool
	}{
		{"co2 minimum", "co2", 0, true},
		{"co2 maximum", "co2", 100000, true},
		{"co2 too high", "co2", 100001, false},
		{"cold possible", "temperature", -100, true},
		{"too cold", "temperature", -101, false},
		{"too hot", "temperature", 151, false},
		{"saturation", "humidity", 100, true},
		{"over saturation", "humidity", 101, false},
		{"NaN", "humidity", math.NaN(), false},
		{"infinity", "co2", math.Inf(1), false},
		{"unknown", "pressure", 1, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := ValidValue(test.metric, test.value); got != test.want {
				t.Fatalf("got %v, want %v", got, test.want)
			}
		})
	}
}

func TestCanonicalPayload(t *testing.T) {
	first := Measurement{SensorID: uuid.New(), Metric: "co2", Value: 800}
	second := Measurement{SensorID: uuid.New(), Metric: "temperature", Value: math.Copysign(0, -1)}
	event := Event{
		DeviceID: uuid.New(), EventID: uuid.New(), RecordedAt: time.Now().UTC(),
		ReceivedAt: time.Now().UTC(), Measurements: []Measurement{first, second},
	}
	hash, err := payloadHash(event)
	if err != nil {
		t.Fatal(err)
	}
	event.Hash = hash
	second.Value = 0
	event.Measurements = []Measurement{second, first}
	event.RecordedAt = event.RecordedAt.In(time.FixedZone("UTC+7", 7*3600))
	event.ReceivedAt = event.ReceivedAt.Add(time.Second)
	if err := event.Validate(); err != nil {
		t.Fatalf("equivalent retry was rejected: %v", err)
	}
	event.Measurements[1].Value++
	if err := event.Validate(); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("modified payload was accepted: %v", err)
	}
}
