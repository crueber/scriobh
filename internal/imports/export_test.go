package imports

// FailAfter makes an apply fail after n documents, for tests.
func FailAfter(f func(inserted int) error) { failAfter = f }
