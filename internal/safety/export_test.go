package safety

// What the external tests (package safety_test) need to see that this package does not
// export. The tests are external because they seed memories through remember, which sits
// above this package.

// ListCopies is listCopies: the copies on disk, newest first.
func (s *Store) ListCopies() ([]SafetyCopy, error) { return s.listCopies() }

// Rotate is rotate: it deletes the copies rotation does not keep and returns their paths.
func (s *Store) Rotate(copies []SafetyCopy) []string { return s.rotate(copies) }
