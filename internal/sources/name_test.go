package sources

import "testing"

func TestVersionedName(t *testing.T) {
	for _, c := range []struct{ asset, tag, want string }{
		{"k8sgpt_amd64.rpm", "v0.4.1", "k8sgpt_amd64-0.4.1.rpm"},
		{"k8sgpt_amd64.rpm", "V0.4.1", "k8sgpt_amd64-0.4.1.rpm"},
		{"veracrypt-1.26.29-CentOS-6-x86_64.rpm", "eraCrypt_1.26.29", "veracrypt-1.26.29-CentOS-6-x86_64.rpm"},
		{"Drop.Desktop.Client-0.4.0-1.x86_64.rpm", "0.4.0-rc-6", "Drop.Desktop.Client-0.4.0-1.x86_64.rpm"},
		{"tool_amd64.rpm", "", "tool_amd64.rpm"},
		{"Tool_AMD64.RPM", "1.2.3", "Tool_AMD64-1.2.3.rpm"},
	} {
		if got := VersionedName(c.asset, c.tag); got != c.want {
			t.Errorf("VersionedName(%q,%q)=%q want %q", c.asset, c.tag, got, c.want)
		}
	}
}
