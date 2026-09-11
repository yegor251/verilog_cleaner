package main

// ProtectedDefines lists macro names that the cleaner must never remove, even
// when they are not used anywhere in the tree (for example documented build
// options or recommended-configuration switches). Each entry is a plain macro
// name string, e.g. "SCR1_RV32IMC_MAX".
var ProtectedDefines = []string{
	// "SCR1_CSR_REDUCED_CNT",
}
