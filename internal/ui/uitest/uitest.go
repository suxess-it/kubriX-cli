// Package uitest provides a scripted ui.UI for testing flows without a terminal.
package uitest

import (
	"context"
	"fmt"
	"io"
	"slices"
	"sort"
	"sync"

	"github.com/suxess-it/kubrix-cli/internal/ui"
)

type abort struct{}

// Abort as an answer cancels the form, like the user pressing ctrl+c.
var Abort = abort{}

// AnswerError is returned when a scripted answer would not have been accepted by the form.
type AnswerError struct {
	Key string
	Err error
}

func (e *AnswerError) Error() string { return fmt.Sprintf("answer to %q rejected: %v", e.Key, e.Err) }
func (e *AnswerError) Unwrap() error { return e.Err }

// UI answers forms from Answers and records everything a flow shows. A field with no answer keeps its default,
// which is how a user who just presses enter answers it. Answers are checked like a real form checks them:
// validators run, and a Choice only accepts one of its options.
type UI struct {
	// Answers maps a field Key to its answer: a string (Input, Choice), []string (Choices), bool (Confirmation)
	// or Abort. The same answer is used every time a field with that Key is asked.
	Answers map[string]any

	mu       sync.Mutex
	used     map[string]bool
	asked    []string
	forms    int
	infos    []string
	warns    []string
	oks      []string
	headings []string
	steps    []string
}

var _ ui.UI = (*UI)(nil)

// New returns a UI with the given answers.
func New(answers map[string]any) *UI { return &UI{Answers: answers} }

// Forms is how many times the flow asked a form.
func (u *UI) Forms() int { u.mu.Lock(); defer u.mu.Unlock(); return u.forms }

// Asked lists the keys of the fields that were shown, in order. Fields of hidden pages are not shown.
func (u *UI) Asked() []string { u.mu.Lock(); defer u.mu.Unlock(); return slices.Clone(u.asked) }

func (u *UI) Infos() []string    { u.mu.Lock(); defer u.mu.Unlock(); return slices.Clone(u.infos) }
func (u *UI) Warns() []string    { u.mu.Lock(); defer u.mu.Unlock(); return slices.Clone(u.warns) }
func (u *UI) OKs() []string      { u.mu.Lock(); defer u.mu.Unlock(); return slices.Clone(u.oks) }
func (u *UI) Headings() []string { u.mu.Lock(); defer u.mu.Unlock(); return slices.Clone(u.headings) }

// Steps lists the titles of the steps that ran.
func (u *UI) Steps() []string { u.mu.Lock(); defer u.mu.Unlock(); return slices.Clone(u.steps) }

// Unused lists answers that no asked field used, which usually means a typo in a key.
func (u *UI) Unused() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	var keys []string
	for k := range u.Answers {
		if !u.used[k] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

func (u *UI) Ask(form ui.Form) error {
	u.mu.Lock()
	u.forms++
	u.mu.Unlock()
	for _, page := range form.Pages {
		if page.Hidden != nil && page.Hidden() {
			continue
		}
		for _, field := range page.Fields {
			if err := u.answer(field); err != nil {
				return err
			}
		}
	}
	return nil
}

// answer returns the scripted answer for key and whether there is one; ok=false means keep the default.
func (u *UI) lookup(key string) (answer any, ok bool, err error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.asked = append(u.asked, key)
	answer, ok = u.Answers[key]
	if !ok {
		return nil, false, nil
	}
	if u.used == nil {
		u.used = map[string]bool{}
	}
	u.used[key] = true
	if answer == any(Abort) {
		return nil, true, ui.ErrAborted
	}
	return answer, true, nil
}

func (u *UI) answer(field ui.Field) error {
	switch f := field.(type) {
	case ui.Input:
		a, ok, err := u.lookup(f.Key)
		if err != nil {
			return err
		}
		if ok {
			s, isString := a.(string)
			if !isString {
				return &AnswerError{f.Key, fmt.Errorf("want a string, got %T", a)}
			}
			*f.Value = s
		}
		if f.Validate != nil {
			if err := f.Validate(*f.Value); err != nil {
				return &AnswerError{f.Key, err}
			}
		}
	case ui.Choice:
		a, ok, err := u.lookup(f.Key)
		if err != nil {
			return err
		}
		if ok {
			s, isString := a.(string)
			if !isString {
				return &AnswerError{f.Key, fmt.Errorf("want a string, got %T", a)}
			}
			*f.Value = s
		}
		// Like huh, a default that is not an option falls back to the first option.
		if !slices.ContainsFunc(f.Options, func(o ui.Option) bool { return o.Value == *f.Value }) {
			if ok || len(f.Options) == 0 {
				return &AnswerError{f.Key, fmt.Errorf("%q is not one of the options", *f.Value)}
			}
			*f.Value = f.Options[0].Value
		}
	case ui.Choices:
		a, ok, err := u.lookup(f.Key)
		if err != nil {
			return err
		}
		if ok {
			sel, isSlice := a.([]string)
			if !isSlice {
				return &AnswerError{f.Key, fmt.Errorf("want a []string, got %T", a)}
			}
			*f.Value = sel
		} else if len(*f.Value) == 0 {
			for _, o := range f.Options {
				if o.Selected {
					*f.Value = append(*f.Value, o.Value)
				}
			}
		}
		for _, v := range *f.Value {
			if !slices.ContainsFunc(f.Options, func(o ui.Option) bool { return o.Value == v }) {
				return &AnswerError{f.Key, fmt.Errorf("%q is not one of the options", v)}
			}
		}
		if f.Validate != nil {
			if err := f.Validate(*f.Value); err != nil {
				return &AnswerError{f.Key, err}
			}
		}
	case ui.Confirmation:
		a, ok, err := u.lookup(f.Key)
		if err != nil {
			return err
		}
		if ok {
			b, isBool := a.(bool)
			if !isBool {
				return &AnswerError{f.Key, fmt.Errorf("want a bool, got %T", a)}
			}
			*f.Value = b
		}
	default:
		return fmt.Errorf("uitest: unsupported field %T", field)
	}
	return nil
}

func (u *UI) Step(ctx context.Context, title string, fn func(context.Context) error) error {
	u.mu.Lock()
	u.steps = append(u.steps, title)
	u.mu.Unlock()
	return fn(ctx)
}

func (u *UI) Begin(string) func(error) { return func(error) {} }
func (u *UI) Logs() io.Writer          { return io.Discard }

func (u *UI) add(to *[]string, m string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	*to = append(*to, m)
}

func (u *UI) Heading(m string) { u.add(&u.headings, m) }
func (u *UI) Info(m string)    { u.add(&u.infos, m) }
func (u *UI) OK(m string)      { u.add(&u.oks, m) }
func (u *UI) Warn(m string)    { u.add(&u.warns, m) }
