package db

import (
	"database/sql/driver"

	"github.com/crueber/scriobh/internal/fold"
	sqlite "modernc.org/sqlite"
)

// scr_fold(s) is text.FoldTitle as a SQL function, so a query's search and its order match and sort text the way the Go code does: SQLite's
// lower() and LIKE fold ASCII only, and an Irish name with a fada would not match its own capitals. It is registered with the driver when this
// package loads, so every connection Open makes has it. It is for queries only: no schema or index uses it.
func init() {
	sqlite.MustRegisterScalarFunction("scr_fold", 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		s, ok := args[0].(string)
		if !ok {
			return nil, nil
		}
		return fold.Title(s), nil
	})
}
