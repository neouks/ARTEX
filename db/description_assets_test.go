package db

import "testing"

func TestDescriptionEvidenceLinkFormatting(t *testing.T) {
	const plain = "目标：https://xxx.xxx.net/"
	const markdown = "目标：[https://xxx.xxx.net/](https://xxx.xxx.net/)"
	for _, tc := range []struct {
		name, description, goal, evidence string
		ok                                bool
	}{
		{"literal", plain, "", plain, true},
		{"added markdown", plain, "", markdown, true},
		{"removed markdown", markdown, "", plain, true},
		{"goal markdown", "", markdown, plain, true},
		{"autolink", "目标：<https://xxx.xxx.net/>", "", markdown, true},
		{"invented prefix", "https://xxx.xxx.net/", "", markdown, false},
		{"invented host", plain, "", "目标：https://other.net/", false},
		{"different destination", plain, "", "目标：[https://xxx.xxx.net/](https://other.net/)", false},
		{"hidden label", "目标：[禁止测试](https://xxx.xxx.net/)", "", plain, false},
		{"denied source", "禁止测试 https://xxx.xxx.net/", "", "[https://xxx.xxx.net/](https://xxx.xxx.net/)", false},
		{"denied markdown source", "禁止测试 [https://xxx.xxx.net/](https://xxx.xxx.net/)", "", "https://xxx.xxx.net/", false},
		{"reference source", "参考：https://xxx.xxx.net/", "", "<https://xxx.xxx.net/>", false},
		{"empty", plain, "", " ", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := validateDescriptionEvidence(tc.description, tc.goal, tc.evidence)
			if (err == nil) != tc.ok {
				t.Fatalf("evidence=%q error=%v, want success=%v", got, err, tc.ok)
			}
			if tc.ok && got != plain {
				t.Fatalf("normalized evidence=%q, want %q", got, plain)
			}
		})
	}
}
