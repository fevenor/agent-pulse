package adapters

import "testing"

func TestHostCompatibilityFollowsVersionPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, actual, min, max string
		allow, warn            bool
	}{
		{"同线新补丁", "1.2.99", "1.2.3", "1.2.3", true, false},
		{"同线旧补丁", "1.2.0", "1.2.3", "1.2.3", true, false},
		{"新次版本", "1.3.0", "1.2.3", "1.2.3", true, true},
		{"旧次版本", "1.1.99", "1.2.3", "1.2.3", true, true},
		{"新主版本", "2.0.0", "1.2.3", "1.2.3", false, false},
		{"旧主版本", "0.9.0", "1.2.3", "1.2.3", false, false},
		{"只有下限不放宽主版本", "2.0.0", "1.2.3", "", false, false},
		{"只有上限不放宽主版本", "2.0.0", "", "1.2.3", false, false},
		{"只有下限的新次版本", "1.3.0", "1.2.3", "", true, true},
		{"只有上限的旧次版本", "1.1.0", "", "1.2.3", true, true},
		{"无版本声明沿用不限", "5.6.7", "", "", true, false},
		{"预发布仅提示", "1.2.4-alpha.1", "1.2.3", "1.2.3", true, true},
		{"非法版本", "unknown", "1.2.3", "1.2.3", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			warning, err := hostCompatibility(tc.actual, Manifest{AgentMin: tc.min, AgentMax: tc.max})
			if (err == nil) != tc.allow || (warning != "") != tc.warn {
				t.Fatalf("版本政策不符：警告=%q，错误=%v", warning, err)
			}
		})
	}
}

func TestManifestRejectsReversedHostVersionRange(t *testing.T) {
	m := Manifest{ID: "sample", Version: "1.0.0", Format: 1, Protocol: 1, MinBridge: "0.5.0", Platform: "any", Kind: "executable", Entry: "adapter", Command: "sample", ConfigStyle: "hooks", AgentMin: "2.0.0", AgentMax: "1.0.0"}
	if err := m.Validate(); err == nil {
		t.Fatal("不应接受颠倒的宿主版本区间")
	}
}
