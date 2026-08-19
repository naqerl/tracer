# Development

- To validate written code run `make vet`
- To test written code run `make test`
- Favor make target and DO NOT RUN `go vet`, `gofmt`, `go fmt` or any other raw `go` commands to
  - lint
  - format
  - build

# Unrelated changes

If any `make` target returns some issues from the code that you have not touched, DO NOT "nah, that's not me, i would not touch it". Instead fix it or interrupt and ask operator about how should it be fixed if not obvious. I do not care about who written the code that does not work, if it does not work for you - it's your responsibility to fix it.

# Skeletons

1. Never cover static text with skeletons — only dynamic data may be replaced by placeholder rectangles.
2. Always reuse the real component template for a skeleton instead of hand-rolling a special look-alike one.
3. Do not add skeletons to content that sits at the end of the page.

# Go Conventions

## Code Comments

Comments must be short, concise, and describe the result the function, struct, or field provides — not how it got there. Never reference previous behaviour, removed code, or historical context: git history already records that. If a comment explains a decision the code makes (e.g. "no load trigger so the first poll has no fetch+blink"), cut it unless the reader genuinely cannot see the decision from the code alone. When in doubt, delete the comment.

Doc comments must purely explain the intent of the abstraction and must never leak information about what consumers exist or what they do. Trivial DTOs and structs may need no comment at all — if the type name and fields already convey their meaning, leave it bare.

## Raw DB Calls in Tests

All raw database calls are forbidden in tests when a public method exists that reaches the same goal. Use the public service API (e.g. `ingestSvc.StoreLog` or `panelSvc.GetLogGroups`) instead of sqlc queries / `db.New(...)` / `handle.Write(...)` directly. This keeps tests exercising the same paths production uses and prevents the DB layer from being a testing shortcut.

## Error Handling Convention

Always use **two consecutive ifs** — never combine with `&&`:
```go
if errors.Is(err, sql.ErrNoRows) {
    return nil, nil  // or handle gracefully
}
if err != nil {
    return nil, fmt.Errorf("context: %w", err)
}
```
This applies to all error checking: sentinel first (via `errors.Is`), unexpected errors second.

When handling errors in web handlers, always use a `switch` statement on sentinel errors — never `if`/`else if` chains:
```go
// GOOD
switch {
case errors.Is(err, ErrNotFound):
    http.Error(w, "not found", http.StatusNotFound)
case errors.Is(err, ErrNoPermission):
    http.Error(w, "access denied", http.StatusForbidden)
case err != nil:
    http.Error(w, "internal error", http.StatusInternalServerError)
default:
    // success
}
```

## Wrap Errors with Context

When wrapping errors with `fmt.Errorf`, use the format `"could not X: %w"` where X is a concise description of the action that failed:
```go
// GOOD
if err != nil {
    return fmt.Errorf("could not query user: %w", err)
}
if err != nil {
    return fmt.Errorf("could not marshal JSON: %w", err)
}

// BAD
return fmt.Errorf("failed to query user: %w", err)
return fmt.Errorf("error: %w", err)
return err  // no context at all
```

## Never Ignore Errors

Errors must never be silently ignored. At minimum, log the error with `slog.Error` and context. Almost always, the error should be returned to the caller wrapped in `fmt.Errorf` with descriptive context.
```go
// GOOD
if err := json.Unmarshal(data, &result); err != nil {
    return fmt.Errorf("could not unmarshal response: %w", err)
}

// GOOD — defer with error logging
defer func() {
    if err := db.Close(); err != nil {
        slog.Error("failed to close database", "error", err)
    }
}()
```

## Struct Visibility

- Only export structs referenced by name in external packages.
- Unexported structs with exported fields work correctly with `json.Marshal`/`json.Unmarshal`.

## File Organization (Go)

Code should be readable from head to tail without jumping back. Within each file, declare elements in this order:

1. **Main struct** — the primary type the file exists for (e.g. `Service`, `Generator`).
2. **Dependency interfaces** — interfaces the main struct uses, defined below it (never before).
3. **Factory function** — constructor for the main struct (e.g. `NewService`).
4. **Public methods** — exported methods on the main struct.
5. **Private helpers** — unexported functions and types used by the public methods.

In test files: test functions first, then the helpers they use at the bottom.

## Avoid Deep Nesting — Use Early Returns

Never write deeply nested conditionals. Invert conditions and return early:
```go
// BAD: deeply nested
if condition {
    // ... 10+ lines of happy path
} else {
    return nil
}

// GOOD: early return flattens the happy path
if !condition {
    return nil
}
// ... flat happy path
```

Note: if the same sentinel (e.g. `sql.ErrNoRows`) gates further branching on related data, nesting is natural — do not duplicate the check just to avoid nesting.

## Switch Default Branch — Defensive Programming

Every `switch` statement must have a `default` branch. The default branch must either return an error (for exhaustive enumeration of domain values) or contain explicit handling logic (for non-exhaustive cases). Never let a switch silently fall through.

```go
// GOOD
switch a := a.(type) {
case domain.System:
    return nil
case domain.User:
    if a.Role == domain.RoleAdmin {
        return nil
    }
    return ErrAccessDenied
default:
    return ErrUnexpectedActor
}
```

## Don't Create One-Line Alias Functions

Never create a function that is just an alias for a single call. Inline the call directly:
```go
// BAD
func (s *Service) saveUserZoom(ctx, zoomID, userID) error {
    return db.New(s.db).SaveUserZoom(...)
}

// GOOD: inline db.New(s.db).SaveUserZoom(...) at call sites
```

## Never Create Wrapper Functions That Swallow Errors

Do not create one-line wrapper functions that simply delegate to a standard library function while swallowing or replacing the error. Call the standard library function directly and handle errors properly at the call site.

```go
// BAD — unnecessary wrapper that swallows the real error
func marshalJSON(v any) []byte {
    b, _ := json.Marshal(v)  // error swallowed!
    return b
}

// GOOD — call json.Marshal directly and handle the error
b, err := json.Marshal(v)
if err != nil {
    return fmt.Errorf("could not marshal request: %w", err)
}
```

## Consumer-Declared Interfaces

The package that depends on a capability declares the interface — never the package that provides the implementation. This is the Go proverb "accept interfaces, return structs" applied at package boundaries.

```go
// GOOD — consumer defines the interface
// consumer/foo.go
type FooService interface {
    DoSomething(ctx context.Context, arg string) (Result, error)
}

// provider/provider.go — concrete struct, no interface
type Provider struct { ... }
func New(ctx, ...) (*Provider, error) { ... }
func (p *Provider) DoSomething(ctx, arg string) (Result, error) { ... }
```

The producer exports concrete types — a struct and its constructor. It knows nothing about the consumer's interface. The consumer imports the producer's concrete types for data types and defines its own interface for dependency injection.

Rationale:
- The producer doesn't know how its output will be used — it shouldn't prescribe the interface.
- The consumer knows exactly what it needs — it declares the minimal contract.
- Multiple consumers can define different interfaces over the same producer without the producer accumulating methods.
- Testing: the consumer can mock its own interface without touching the producer.

## Adapter Pattern

When the consumer's interface doesn't match an existing concrete type's method signature, create an adapter in an `adapters.go` file **within the consumer's package** (not in the producer's). The adapter implements the consumer's interface by wrapping the concrete type.

Rules:
- **The interface lives in the same file that uses it**, not in a separate file.
- The adapter lives in `adapters.go` in the same package.
- The adapter should be trivial — one method per adapted method, no extra logic.
- The concrete type being adapted never changes to match the interface — the adapter bridges the gap.

## Factory Functions for Pointer Types

Every exported struct that is consumed via a pointer must have a constructor (factory) function. Never initialize a pointer-type struct with a literal outside its package.

```go
// GOOD — factory function
func NewService(dep *SomeDep) *Service {
    return &Service{dep: dep}
}

// BAD — struct literal outside the package
svc := &Service{Dep: someDep}
```

Rationale:
- The factory can validate inputs, set defaults, and encapsulate construction complexity.
- Consumers don't need to know about unexported fields or initialization invariants.

## Same-Type Argument Grouping

When consecutive function parameters share the same type, group them with a single type annotation instead of repeating it:

```go
// GOOD — grouped
func NewAgent(ctx context.Context, baseURL, apiKey, model string) (*Agent, error) { ... }

// BAD — repeated
func NewAgent(ctx context.Context, baseURL string, apiKey string, model string) (*Agent, error) { ... }
```

## Don't Create Tiny Files

Do not create files that contain only 3–5 lines of code unless the file represents a new shared package with its own distinct responsibility. Small interfaces, trivial type aliases, and single-function wrappers belong in the file where they are used — not in a separate file.

Rationale:
- Every file adds cognitive overhead: imports, navigation, mental context switching.
- Small interfaces are implementation details of the struct that uses them — they don't warrant their own file.

## Context-Aware Channel Sends

When sending to a channel, always use `select` with `ctx.Done()` to prevent blocking:
```go
select {
case ch <- msg:
case <-ctx.Done():
    return ctx.Err()
}
```

## Use `t.Context()` in Tests

Always use `t.Context()` instead of `context.Background()` in test functions.

## Deferred Rollback Pattern

```go
defer func() {
    if err := tx.Rollback(); err != nil {
        if !errors.Is(err, sql.ErrTxDone) {
            slog.Warn("transaction rollback error", "error", err)
        }
    }
}()
```
