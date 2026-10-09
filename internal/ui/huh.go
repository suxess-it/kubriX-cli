package ui

import "github.com/charmbracelet/huh"

// toHuh renders a Form with huh; it is the only place that knows how fields look.
func toHuh(f Form) *huh.Form {
	groups := make([]*huh.Group, 0, len(f.Pages))
	for _, p := range f.Pages {
		fields := make([]huh.Field, 0, len(p.Fields))
		for _, field := range p.Fields {
			fields = append(fields, field.huhField())
		}
		g := huh.NewGroup(fields...)
		if p.Title != "" {
			g = g.Title(p.Title)
		}
		if p.Description != "" {
			g = g.Description(p.Description)
		}
		if p.Hidden != nil {
			g = g.WithHideFunc(p.Hidden)
		}
		groups = append(groups, g)
	}
	return huh.NewForm(groups...)
}

func huhOptions(options []Option) []huh.Option[string] {
	out := make([]huh.Option[string], len(options))
	for i, o := range options {
		out[i] = huh.NewOption(o.Label, o.Value).Selected(o.Selected)
	}
	return out
}

func (i Input) huhField() huh.Field {
	f := huh.NewInput().Title(i.Title).Value(i.Value)
	if i.DescriptionFunc != nil {
		f = f.DescriptionFunc(i.DescriptionFunc, i.Watch)
	} else {
		f = f.Description(i.Description)
	}
	if i.Secret {
		f = f.EchoMode(huh.EchoModePassword)
	}
	if i.Validate != nil {
		f = f.Validate(i.Validate)
	}
	return f
}

func (s Choice) huhField() huh.Field {
	return huh.NewSelect[string]().Title(s.Title).Description(s.Description).Options(huhOptions(s.Options)...).Value(s.Value)
}

func (m Choices) huhField() huh.Field {
	f := huh.NewMultiSelect[string]().Title(m.Title).Description(m.Description).Options(huhOptions(m.Options)...).Value(m.Value)
	if m.Height > 0 {
		f = f.Height(m.Height)
	}
	if m.Validate != nil {
		f = f.Validate(m.Validate)
	}
	return f
}

func (c Confirmation) huhField() huh.Field {
	return huh.NewConfirm().Title(c.Title).Description(c.Description).Affirmative("Yes").Negative("No").Value(c.Value)
}
