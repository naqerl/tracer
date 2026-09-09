---
name: htmx-forms
description: Use when building or reworking HTMX forms in the panel web UI (app creation form, notifier setup in /settings, settings forms). Covers swap targets, field-level status indicators, error rendering, and detection-on-input (debounced) patterns. Refer to apps/panel/web/handlers/settings/view/settings.templ (Forgejo notifier) and apps/panel/web/handlers/panel/view/collector_app_form.templ for live examples.
---

# HTMX form conventions

Forms in the panel are server-rendered Go `templ` fragments driven by HTMX.
This document is the reference for the conventions used across the app.

## Response handling config

`templates/shared.templ` (`Head`) pins the global HTMX response handling:

- `204` — no swap (action done, mutations happen via headers).
- `[23]..` — swap the response body.
- `422` — swap the response body (validation errors re-render the form in place).
- `[45]..` — no swap, treated as an error.
- anything else — no swap.

So handlers render validation/errors as a fragment and return a 2xx/422, never
relying on error pages for in-form failures.

## Form = fragment with an outer swap target

The whole form lives in a container element and every submission swaps that
container in place. Handlers render only the fragment, never the full page.

```templ
<div id="app-form-container" class="space-y-6">
	<form hx-post="/panel/collectors/{collector}/apps"
		  hx-target="#app-form-container"
		  hx-swap="innerHTML" class="space-y-6">
		...
	</form>
</div>
```

Consequences:

- Full-page loaders render the fragment once inside `templates.Layout`; all
  subsequent updates are swaps.
- A handler that needs to apply progressive state (e.g. a format-guess result)
  re-renders the same `#...-form-container` fragment with new props.
- For a "page" whose content is an HTMX poll/fragment, embed a skeleton that
  matches the real component (see AGENTS.md "Skeletons").

## Labels carry status and indicators

Every field label keeps `Required` + `FieldStatus` + `Indicator` together so
per-field feedback renders right next to the label text:

```templ
<label class="block text-sm text-fg-secondary mb-1">
	Log source
	@templates.Required()
	@templates.FieldStatus("app-format-status", p.Guess.Status, p.Guess.StatusOK, false)
	@templates.Indicator("app-format-indicator", "Guessing format…")
</label>
```

- `templates.Required()` — red `*` marking a required field.
- `templates.FieldStatus(id, text, ok, oob)` — status line right of the label.
  Hidden while `text == ""`; green text when `ok`, red otherwise. Pass
  `oob=true` when a response must update it out-of-band.
- `templates.Indicator(id, text)` — inline spinner, hidden until the element
  referencing its id via `hx-indicator` starts a request.

The app form's **not-found warning belongs to the left of the label** so it is
visibly distinct from the right-hand status line. Render it as the first
element inside the label and suppress the right-hand status while set:

```templ
<label class="block text-sm text-fg-secondary mb-1">
	if p.Guess.NotFound {
		<span class="text-red-500 font-medium mr-1">not found</span>
	}
	Log source
	@templates.Required()
	@templates.FieldStatus("app-format-status", tern(p.Guess.NotFound, "", p.Guess.Status), p.Guess.StatusOK, false)
	@templates.Indicator("app-format-indicator", "Guessing format…")
</label>
```

Field usage (Forgejo):

```templ
<input ... hx-trigger="change, input changed delay:1s"
		hx-post="/settings/notifiers/forgejo/lookup"
		hx-target="#forgejo-lookup"
		hx-swap="outerHTML"
		hx-indicator="#forgejo-fetching"/>
```

## Errors

Two layers, both modeled on the /settings notifier (Forgejo) form:

1. **Per-field async feedback** — `FieldStatus` right of the label, set by the
   fragment the lookup/detection request returns. Strings render in red unless
   `ok` is true.
2. **Submit errors** — the form targets the next `.error` sibling:
   ```templ
   <form hx-post="..." hx-target="next .error">...</form>
   <span class="error"></span>
   ```
   The handler writes the message into that span (e.g. via `hx-reswap`), or
   re-renders the whole fragment with `Status`/`StatusOK` props as 422.

Inline failure messages inside swapped fragments use
`<p class="text-sm text-red-500">…</p>` (see `ForgejoSelects.Status`).

## Detection on input with debounce

For "type a name, guess while typing" flows (collector app form), the first
field is a free-form app **name**. On blur the same name is set into the **log
source** field; the log source field then guesses the format against the
collector **on input** with a debounce (`input changed delay:500ms`, mirroring
the forgejo `input changed delay:1s` pattern). The copy-and-trigger happens
client-side so the log source is visibly populated before the request
round-trip; editing the log source afterwards re-guesses while typing:

```templ
<input type="text" name="name" id="app-name-input"
	   hx-on:blur="document.getElementById('app-service-input').value=this.value;htmx.trigger('#app-service-input','input')"/>

<input type="text" name="service" id="app-service-input" value={ p.Guess.Service }
	   hx-post={ previewurl }
	   hx-trigger="input changed delay:500ms"
	   hx-include="#app-name-input"
	   hx-target="#app-form-container"
	   hx-swap="innerHTML"
	   hx-indicator="#app-format-indicator"/>
```

(`hx-on:blur` needs htmx 1.9+/2.x; `htmx.trigger` fires the target's existing
`hx-trigger` handler.)

The endpoint re-renders the same fragment carrying the outcome. The outcome
renders **right under the log source field** (via `AppFormatFeedback`):

- **conflict** — a favour box listing each detected value as a button; the
  buttons include the whole form (`hx-include="closest form"`) and re-run the
  preview with `favour_field`/`favour_value` in `hx-vals`.
- **found + auto-guessed** — a green `Detected <source> / <parser> format`
  banner.
- **not found** — the label's left-hand `not found` warning (see above);
  nothing else renders under the field.

The **advanced configuration** panel is hidden until a format is guessed. It
is shown only when the guess succeeded (or on edit) and carries the
**prefilled detected values** plus an **example log** drawn from the sample
lines:

```templ
if p.Guess.Detected || p.IsEdit {
	<div class="border border-bg-secondary/70 p-4">
		<h3 class="text-fg font-medium mb-3">Advanced configuration</h3>
		...source type / source params / parser type / parser params, value={ p.Guess.* }...
		if len(p.Guess.Samples) > 0 {
			<pre class="...">{ p.Guess.Samples }</pre>
		}
	</div>
}
```

So the editable fields double as the detection report: nothing to edit until a
format exists.

## Post-action navigation (204)

Actions that navigate afterwards return `204 No Content` with headers instead
of an HTML body:

```go
w.Header().Set("HX-Redirect", ...)
w.WriteHeader(http.StatusNoContent)
```

The 204 response handling suppresses the (empty) swap.