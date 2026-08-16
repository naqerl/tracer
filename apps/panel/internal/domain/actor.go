package domain

import "errors"

// Role is the privilege level of an authenticated user.
type Role string

const (
	RoleUser  Role = "user"
	RoleAdmin Role = "admin"
)

// Actor is any caller of a service method: an authenticated User, the
// internal System, or an unauthenticated caller.
type Actor interface {
	IsActor()
}

// System is the internal, automated caller with full access.
type System struct{}

// IsActor marks System as an Actor.
func (System) IsActor() {}

// Unauthenticated is a caller with no valid session or API key. Services deny
// it by default.
type Unauthenticated struct{}

// IsActor marks Unauthenticated as an Actor.
func (Unauthenticated) IsActor() {}

// User is an authenticated caller with an id, a role, and display fields.
type User struct {
	ID        int64
	Username  string
	CreatedAt int64
	Role      Role
}

// IsActor marks User as an Actor.
func (User) IsActor() {}

// ErrUnexpectedActor is returned by actor type switches when an unknown actor
// type appears.
var ErrUnexpectedActor = errors.New("unexpected actor")
