package repository

import "errors"

// ErrNotFound is returned when an entity does not exist.
var ErrNotFound = errors.New("not found")
