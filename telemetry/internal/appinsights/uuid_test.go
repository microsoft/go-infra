package appinsights

import (
	"regexp"
	"testing"
)

func TestNewUUID(t *testing.T) {
	uuid := newUUID()

	// Check if the UUID matches the expected format.
	const uuidRegex = `^[a-f0-9]{8}-[a-f0-9]{4}-4[a-f0-9]{3}-[89ab][a-f0-9]{3}-[a-f0-9]{12}$`
	matched := regexp.MustCompile(uuidRegex).MatchString(uuid)
	if !matched {
		t.Errorf("Generated UUID does not match the expected format: %s", uuid)
	}

	// Ensure that multiple calls to newUUID generate unique values.
	uuid2 := newUUID()
	if uuid == uuid2 {
		t.Errorf("Generated UUIDs are not unique: %s and %s", uuid, uuid2)
	}
}
