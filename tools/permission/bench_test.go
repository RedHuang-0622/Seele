package permission

import "testing"

// Benchmarks for the permission decision hot path. Run with:
//
//	go test -bench . -benchmem -run '^$' ./tools/permission
//	go test -bench . -benchmem -run '^$' -cpuprofile cpu.out ./tools/permission
func BenchmarkCheckForRoutedAllow(b *testing.B) {
	pc := NewPermissionChecker(matrixConfig())
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if got := pc.CheckFor("main", "read_doc", `{}`); got != ResultAllow {
			b.Fatalf("CheckFor = %v", got)
		}
	}
}

func BenchmarkCheckForMissingBit(b *testing.B) {
	pc := NewPermissionChecker(matrixConfig())
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if got := pc.CheckFor("sub", "write_doc", `{}`); got != ResultAsk {
			b.Fatalf("CheckFor = %v", got)
		}
	}
}

func BenchmarkCheckForRulesAndBits(b *testing.B) {
	var write = BitWrite
	pc := NewPermissionChecker(PermissionConfig{
		Mode:     ModeManual,
		Groups:   []PermissionGroup{{Name: "rw", Match: []string{"write_*"}, Mode: 0, Default: ActionAsk}},
		Rules:    []PermissionRule{{ToolName: "write_*", Action: ActionAllow, Bits: &write}},
		Subjects: map[Subject]SubjectGrant{"rw_user": {Bits: map[string]GrantBit{"rw": {Bits: BitRead | BitWrite}}}},
	})
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		pc.CheckFor("rw_user", "write_doc", `{}`)
	}
}

func BenchmarkVisibleFor(b *testing.B) {
	pc := NewPermissionChecker(matrixConfig())
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = pc.VisibleFor("sub", "write_doc")
	}
}
