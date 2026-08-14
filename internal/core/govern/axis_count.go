package govern

// AxisCount returns the number of AxisEvaluators registered on g,
// including the always-present memory axis added by New.
// CPU and disk axes are added by NewCPUAxis and NewDiskAxis respectively.
//
// Intended for use in tests that verify governor axis wiring.
// Do not call from production code.
func (g *Governor) AxisCount() int {
	return len(g.axes)
}
