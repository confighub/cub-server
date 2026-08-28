package install

import "errors"

// ErrReported marks an error whose explanation has already been printed in a
// readable form. main checks for it so a carefully worded failure does not get a
// generic "error:" line stapled on top of it.
var ErrReported = errors.New("reported")
