//go:build vivadev && (darwin || windows || linux)

package app

import (
	"fmt"

	"github.com/vivaui/viva/gfx"
	"github.com/vivaui/viva/internal/reloadwire"
	"github.com/vivaui/viva/text"
)

// runReloadWorkerIfEnabled reports whether this process is a development
// reload worker and, if so, runs it. The release build has no implementation
// that mentions the protocol package at all (see reload_disabled.go).
func runReloadWorkerIfEnabled(config Config, canvas *gfx.Canvas) (bool, error) {
	if !reloadwire.Enabled() {
		return false, nil
	}
	return true, runReloadWorker(config, canvas)
}

func runReloadWorker(config Config, canvas *gfx.Canvas) error {
	if canvas == nil {
		canvas = &gfx.Canvas{}
	}
	return reloadwire.Serve(newReloadWorkerConfig(config, canvas))
}

func newReloadWorkerConfig(config Config, canvas *gfx.Canvas) reloadwire.WorkerConfig {
	worker := reloadwire.WorkerConfig{
		Window: reloadwire.WindowConfig{
			Title: config.Title, Width: config.Width, Height: config.Height,
			MinWidth: config.MinWidth, MinHeight: config.MinHeight,
			WindowMode: uint8(config.WindowMode), TitlebarHeight: uint16(max(config.TitlebarHeight+0.5, 0)),
			TrafficLightScale: config.TrafficLightScale,
			ClearRed:          config.ClearColor.Red, ClearGreen: config.ClearColor.Green,
			ClearBlue: config.ClearColor.Blue, ClearAlpha: config.ClearColor.Alpha,
			HasClick: config.Paint != nil, HasPointer: config.OnPointer != nil || config.Paint != nil,
			HasKey: config.OnKey != nil, HasShortcut: config.OnShortcut != nil, HasScroll: config.OnScroll != nil, HasModifierTap: config.OnModifierTap != nil,
			HasClose:    config.OnCloseRequest != nil,
			HasSnapshot: config.Reload.Snapshot != nil, HasRestore: config.Reload.Restore != nil,
		},
		Canvas: canvas, Paint: config.Paint,
		Pointer: config.OnPointer, Key: config.OnKey, Shortcut: config.OnShortcut, Scroll: config.OnScroll, ModifierTap: config.OnModifierTap,
		CloseRequest: config.OnCloseRequest,
	}
	if config.Reload.Snapshot != nil {
		worker.Snapshot = func() ([]reloadwire.StateSlot, error) {
			slots, snapshotErr := config.Reload.Snapshot()
			if snapshotErr != nil {
				return nil, snapshotErr
			}
			result := make([]reloadwire.StateSlot, len(slots))
			for index, slot := range slots {
				result[index] = reloadwire.StateSlot{Key: slot.Key, Version: slot.Version, Data: slot.Data}
			}
			return result, nil
		}
	}
	if config.Reload.Restore != nil {
		worker.Restore = func(slots []reloadwire.StateSlot) error {
			values := make([]StateSlot, len(slots))
			for index, slot := range slots {
				values[index] = StateSlot{Key: slot.Key, Version: slot.Version, Data: slot.Data}
			}
			return config.Reload.Restore(values)
		}
	}
	if config.TextInput != nil {
		// The worker owns the application's text editors. It resolves each
		// focused field through config.TextInput, remembers the editor by
		// target ID, and applies forwarded native text events to it.
		textEditors := make(map[gfx.ID]*text.Editor)
		worker.TextInput = func(request reloadwire.TextSyncRequest) *reloadwire.TextSyncResponse {
			context := TextInputContext{
				ID:                 request.ID,
				Bounds:             request.Bounds,
				Layer:              request.Layer,
				Multiline:          request.Multiline,
				ReadOnly:           request.ReadOnly,
				Secure:             request.Secure,
				NavigationKeys:     request.NavigationKeys,
				Terminal:           request.Terminal,
				TypeAhead:          request.TypeAhead,
				TextBounds:         request.TextBounds,
				FontSize:           request.FontSize,
				FontWeight:         request.FontWeight,
				TextColor:          request.TextColor,
				Alignment:          request.Alignment,
				HideNativeOverlays: request.HideNativeOverlays,
			}
			input := config.TextInput(context)
			if input == nil {
				delete(textEditors, request.ID)
				return &reloadwire.TextSyncResponse{HasInput: false}
			}
			if input.Editor != nil {
				input.State = TextStateOf(input.Editor)
				textEditors[request.ID] = input.Editor
			} else {
				delete(textEditors, request.ID)
			}
			return &reloadwire.TextSyncResponse{
				HasInput:            true,
				Bounds:              input.Bounds,
				FontSize:            input.FontSize,
				Weight:              input.Weight,
				Color:               input.Color,
				MonoFace:            input.MonoFace,
				Alignment:           input.Alignment,
				AltSendsEscape:      input.AltSendsEscape,
				KeyReleases:         input.KeyReleases,
				DisableIME:          input.DisableIME,
				NavigationKeys:      input.NavigationKeys,
				CopyOverride:        input.CopyOverride,
				CopyRectangular:     input.CopyRectangular,
				HideNativeOverlays:  input.HideNativeOverlays,
				State: reloadwire.TextState{
					SelectionReversed:   input.State.SelectionReversed,
					Value:                input.State.Value,
					Selection:            reloadwire.TextRange{Start: int32(input.State.Selection.Start), End: int32(input.State.Selection.End)},
					Composition:         reloadwire.TextRange{Start: int32(input.State.Composition.Start), End: int32(input.State.Composition.End)},
					Composing:           input.State.Composing,
					TentativeComposition: input.State.TentativeComposition,
					Redraw:              input.State.Redraw,
				},
			}
		}
		worker.TextEvent = func(request reloadwire.TextEventRequest) (reloadwire.TextState, error) {
			editor, ok := textEditors[request.Target]
			if !ok || editor == nil {
				return reloadwire.TextState{}, fmt.Errorf("text event for unknown target")
			}
			event := TextEvent{
				Kind:                  TextEventKind(request.Kind),
				Rectangular:           request.Rectangular,
				Text:                  request.Text,
				Selection:             TextRange{Start: int(request.Selection.Start), End: int(request.Selection.End)},
				MarkedSelection:       TextRange{Start: int(request.MarkedSelection.Start), End: int(request.MarkedSelection.End)},
				Replacement:           TextRange{Start: int(request.Replacement.Start), End: int(request.Replacement.End)},
				HasReplacement:        request.HasReplacement,
				Command:               TextCommand(request.Command),
				CompositionPhase:      text.CompositionPhase(request.CompositionPhase),
				ReplacementUTF16:     text.UTF16Range{Start: int(request.ReplacementUTF16Start), Length: int(request.ReplacementUTF16Length)},
				HasReplacementUTF16:  request.HasReplacementUTF16,
			}
			event.Apply(editor)
			state := TextStateOf(editor)
			return reloadwire.TextState{
				SelectionReversed:   state.SelectionReversed,
				Value:                state.Value,
				Selection:            reloadwire.TextRange{Start: int32(state.Selection.Start), End: int32(state.Selection.End)},
				Composition:         reloadwire.TextRange{Start: int32(state.Composition.Start), End: int32(state.Composition.End)},
				Composing:            state.Composing,
				TentativeComposition: state.TentativeComposition,
				Redraw:               state.Redraw,
			}, nil
		}
	}
	return worker
}
