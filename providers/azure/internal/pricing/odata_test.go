package pricing

import "testing"

func TestODataString(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"region", "eastus", "'eastus'", false},
		{"valid sku", "GP_Gen5_2", "'GP_Gen5_2'", false},
		{"dots and dashes", "Azure_Redis_Cache-E10.1", "'Azure_Redis_Cache-E10.1'", false},
		{"single quote", "a'b", "", true},
		{"injection attempt", "x' or '1' eq '1", "", true},
		{"space", "East US", "", true},
		{"empty", "", "", true},
		{"newline", "a\nb", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ODataString(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}
