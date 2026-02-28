package functions

import lmf "codexagentbase/lmf/runtime"

func NewRegistry() *lmf.Registry {
	r := lmf.NewRegistry()
	r.MustRegister(IntrinsicModifyCodebase())
	r.MustRegister(TestProgrammaticGuard())
	r.MustRegister(TestNLConditionFunny())
	r.MustRegister(TestNLExecContextMemory())
	r.MustRegister(TestNLConditionIsolation())
	r.MustRegister(TestTypedNLExecEcho())
	return r
}
