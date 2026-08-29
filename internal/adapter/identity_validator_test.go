package adapter

import "testing"

type conversationIDValidatingAdapter struct{ baseAdapter }

func (conversationIDValidatingAdapter) IsValidConversationID(id string) bool {
	return id == "native-good"
}

func TestConversationIDValidatorIsOptionalAndGeneric(t *testing.T) {
	if _, ok := AsConversationIDValidator(baseAdapter{}); ok {
		t.Fatal("base adapter unexpectedly implements the optional conversation-ID validator")
	}
	if !AcceptsConversationID(baseAdapter{}, "provider-opaque") {
		t.Fatal("an adapter without the optional validator must retain its opaque-ID behavior")
	}

	validating := conversationIDValidatingAdapter{}
	validator, ok := AsConversationIDValidator(validating)
	if !ok {
		t.Fatal("validator extension was not discovered")
	}
	if !validator.IsValidConversationID("native-good") || validator.IsValidConversationID("corrupt") {
		t.Fatal("discovered validator did not preserve its native-ID contract")
	}
	if !AcceptsConversationID(validating, "native-good") {
		t.Fatal("valid native ID was rejected")
	}
	if AcceptsConversationID(validating, "corrupt") {
		t.Fatal("corrupt native ID was accepted")
	}
}
