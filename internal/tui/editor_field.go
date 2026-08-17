package tuiapp

import tui "github.com/grindlemire/go-tui"

// editorField wraps go-tui Input/TextArea so Escape cancels the modal form.
// Focused inputs otherwise consume Escape to blur, which leaves the form open.
type editorField struct {
	app      *tui.App
	value    *tui.State[string]
	input    *tui.Input
	textarea *tui.TextArea
	body     bool
	onCancel func()
}

func NewEditorField(p *posthouseApp, index int) *editorField {
	return p.newEditorField(index)
}

func (p *posthouseApp) newEditorField(index int) *editorField {
	field := &editorField{onCancel: p.cancelEditor}
	if index < 0 || index >= len(p.editorFields) {
		return field
	}
	field.value = p.editorFields[index]
	if p.editorFieldIsBody(index) {
		field.body = true
		field.textarea = tui.NewTextArea(
			tui.WithTextAreaValue(field.value),
			tui.WithTextAreaWidth(editorInputWidth),
			tui.WithTextAreaMaxHeight(8),
			tui.WithTextAreaBorder(tui.BorderRounded),
			tui.WithTextAreaSubmitKey(tui.KeyF2),
			tui.WithTextAreaOnSubmit(p.submitEditorText),
		)
		return field
	}
	field.input = tui.NewInput(
		tui.WithInputValue(field.value),
		tui.WithInputWidth(editorInputWidth),
		tui.WithInputBorder(tui.BorderRounded),
		tui.WithInputPlaceholder(p.editorPlaceholder(index)),
		tui.WithInputOnSubmit(p.submitEditorText),
	)
	return field
}

func (e *editorField) Render(app *tui.App) *tui.Element {
	if e.textarea != nil {
		return e.textarea.Render(app)
	}
	if e.input != nil {
		return e.input.Render(app)
	}
	return tui.New()
}

func (e *editorField) BindApp(app *tui.App) {
	e.app = app
	e.bindInner()
}

func (e *editorField) bindInner() {
	if e.app == nil {
		return
	}
	if e.input != nil {
		e.input.BindApp(e.app)
	}
	if e.textarea != nil {
		e.textarea.BindApp(e.app)
	}
}

func (e *editorField) UpdateProps(fresh tui.Component) {
	next, ok := fresh.(*editorField)
	if !ok {
		return
	}
	e.onCancel = next.onCancel
	if e.value == next.value && e.body == next.body {
		return
	}
	e.value = next.value
	e.body = next.body
	e.input = next.input
	e.textarea = next.textarea
}

func (e *editorField) Watchers() []tui.Watcher {
	if e.textarea != nil {
		return e.textarea.Watchers()
	}
	if e.input != nil {
		return e.input.Watchers()
	}
	return nil
}

func (e *editorField) IsFocused() bool {
	if e.textarea != nil {
		return e.textarea.IsFocused()
	}
	if e.input != nil {
		return e.input.IsFocused()
	}
	return false
}

func (e *editorField) KeyMap() tui.KeyMap {
	var km tui.KeyMap
	switch {
	case e.textarea != nil:
		km = e.textarea.KeyMap()
	case e.input != nil:
		km = e.input.KeyMap()
	}
	return remapFocusedEscape(km, func(tui.KeyEvent) {
		if e.onCancel != nil {
			e.onCancel()
		}
	})
}

var (
	_ tui.Component       = (*editorField)(nil)
	_ tui.KeyListener     = (*editorField)(nil)
	_ tui.AppBinder       = (*editorField)(nil)
	_ tui.PropsUpdater    = (*editorField)(nil)
	_ tui.WatcherProvider = (*editorField)(nil)
)

func remapFocusedEscape(km tui.KeyMap, onEscape func(tui.KeyEvent)) tui.KeyMap {
	out := make(tui.KeyMap, 0, len(km)+1)
	for _, binding := range km {
		if binding.Pattern.Key == tui.KeyEscape {
			continue
		}
		out = append(out, binding)
	}
	return append(out, tui.OnFocused(tui.KeyEscape, onEscape))
}
