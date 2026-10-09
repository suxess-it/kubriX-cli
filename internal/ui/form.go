package ui

import (
	"errors"

	"github.com/charmbracelet/huh"
)

// Form is a set of questions asked together. Fields write the user's answers through their Value pointers,
// and the initial value of a pointer is the default answer. Plain and Fullscreen render it with huh;
// uitest answers it from a script.
type Form struct {
	Pages []Page
}

// Page is a group of fields shown together.
type Page struct {
	Title       string
	Description string
	// Hidden skips the page when it returns true; it is evaluated when the page is reached, so it can depend
	// on answers given on earlier pages.
	Hidden func() bool
	Fields []Field
}

// Field is one question: Input, Choice, Choices or Confirmation.
type Field interface {
	huhField() huh.Field
}

// Option is one of the answers of a Choice or Choices field.
type Option struct {
	Label string
	Value string
	// Selected starts the option selected in a Choices field.
	Selected bool
}

// Options makes an Option of each value, labelled by the value itself.
func Options(values ...string) []Option {
	options := make([]Option, len(values))
	for i, v := range values {
		options[i] = Option{Label: v, Value: v}
	}
	return options
}

// Input asks for a line of text.
type Input struct {
	// Key identifies the field to scripted answers; it should be unique within a flow.
	Key         string
	Title       string
	Description string
	// DescriptionFunc replaces Description with text that depends on other answers; Watch is the variable it
	// depends on (a pointer), so the text updates when that answer changes.
	DescriptionFunc func() string
	Watch           any
	// Secret hides what is typed.
	Secret   bool
	Validate func(string) error
	Value    *string
}

// Choice asks for one of Options.
type Choice struct {
	Key         string
	Title       string
	Description string
	Options     []Option
	Value       *string
}

// Choices asks for any number of Options.
type Choices struct {
	Key         string
	Title       string
	Description string
	Options     []Option
	// Height limits the visible rows; 0 means the default.
	Height   int
	Validate func([]string) error
	Value    *[]string
}

// Confirmation asks yes or no.
type Confirmation struct {
	Key         string
	Title       string
	Description string
	Value       *bool
}

// Confirm asks a yes/no question; description carries any warning, since a full-screen form hides the transcript.
func Confirm(u UI, title, description string, def bool) (bool, error) {
	ok := def
	err := u.Ask(Form{Pages: []Page{{Fields: []Field{Confirmation{Key: title, Title: title, Description: description, Value: &ok}}}}})
	return ok, err
}

// Select asks the user to pick one of options; value holds the default and receives the answer.
func Select(u UI, title, description string, options []Option, value *string) error {
	return u.Ask(Form{Pages: []Page{{Fields: []Field{Choice{Key: title, Title: title, Description: description, Options: options, Value: value}}}}})
}

// errAborted maps huh's cancellation to ErrAborted, so callers never see huh's errors.
func errAborted(err error) error {
	if errors.Is(err, huh.ErrUserAborted) {
		return ErrAborted
	}
	return err
}
