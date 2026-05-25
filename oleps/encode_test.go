package oleps_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/abemedia/go-cfb/oleps"
)

func TestMarshal_Errors(t *testing.T) {
	codepage := oleps.Property{ID: 1, Value: oleps.I2(1252)}
	set := func(props ...oleps.Property) oleps.PropertySetStream {
		return oleps.PropertySetStream{PropertySets: []oleps.PropertySet{{Properties: props}}}
	}

	tests := []struct {
		name    string
		input   oleps.PropertySetStream
		want    error
		wantMsg string
	}{
		{"zero sets", oleps.PropertySetStream{}, nil, "invalid property set count"},
		{"three sets", oleps.PropertySetStream{PropertySets: []oleps.PropertySet{{}, {}, {}}}, nil, "invalid property set count"},
		{"two sets unsupported", oleps.PropertySetStream{PropertySets: []oleps.PropertySet{{}, {}}}, oleps.ErrUnsupported, "more than one property set"},
		{"invalid version", oleps.PropertySetStream{Version: 2, PropertySets: []oleps.PropertySet{{}}}, nil, "invalid property set version 2"},
		{"dictionary unsupported", set(codepage, oleps.Property{ID: 0, Value: oleps.I4(7)}), oleps.ErrUnsupported, "named properties"},
		{"code page wrong type", set(oleps.Property{ID: 1, Value: oleps.I4(7)}), nil, "invalid code page property"},
		{"missing code page", set(oleps.Property{ID: 2, Value: oleps.I4(7)}), nil, "missing code page property"},
		{"nil property value", set(codepage, oleps.Property{ID: 2}), nil, "nil property value"},
		{"unknown code page", set(oleps.Property{ID: 1, Value: oleps.I2(12000)}), oleps.ErrUnsupported, "code page 12000"},
		{"cannot encode string", set(codepage, oleps.Property{ID: 2, Value: oleps.LPSTR("日本語")}), nil, "cannot encode string"},
		{"timestamp before year 1601", set(codepage, oleps.Property{ID: 2, Value: oleps.FileTime(time.Date(1600, 1, 1, 0, 0, 0, 0, time.UTC))}), nil, "before year 1601"},
		{"timestamp after year 60056", set(codepage, oleps.Property{ID: 2, Value: oleps.FileTime(time.Date(100000, 1, 1, 0, 0, 0, 0, time.UTC))}), nil, "after year 60056"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := oleps.Marshal(test.input)
			if err == nil {
				t.Fatal("expected an error")
			}
			if test.want != nil && !errors.Is(err, test.want) {
				t.Fatalf("err = %v, want %v", err, test.want)
			}
			if !strings.Contains(err.Error(), test.wantMsg) {
				t.Errorf("err = %q, want message containing %q", err, test.wantMsg)
			}
		})
	}
}
