package ip

// Macvtap represents arguments for link of type macvtap.
type Macvtap struct {
	Macvlan
}

// Add adds new virtual link.
func (macvtap *Macvtap) Add() error {
	return macvtap.add("macvtap", []string{"mode", macvtap.Mode})
}
