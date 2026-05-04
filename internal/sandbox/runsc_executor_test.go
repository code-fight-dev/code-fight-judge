package sandbox

import "testing"

func TestParseDockerMemoryUsageKB(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want int
	}{
		{
			name: "bytes",
			in:   "512B / 256MiB",
			want: 1,
		},
		{
			name: "kib",
			in:   "1536KiB / 256MiB",
			want: 1536,
		},
		{
			name: "mib decimal",
			in:   "1.5MiB / 256MiB",
			want: 1536,
		},
		{
			name: "gib decimal",
			in:   "0.5GiB / 2GiB",
			want: 524288,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseDockerMemoryUsageKB(tt.in)
			if err != nil {
				t.Fatalf("parseDockerMemoryUsageKB() error = %v", err)
			}

			if got != tt.want {
				t.Fatalf("parseDockerMemoryUsageKB() = %d, want %d", got, tt.want)
			}
		})
	}
}
