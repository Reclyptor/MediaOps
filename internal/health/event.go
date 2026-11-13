package health

import "time"

// Event kinds.
const (
	KindUSBDeviceGone = "usb_device_gone"
	KindSRNodeGone    = "sr_node_gone"
	KindLinkDropped   = "link_dropped"
	KindDmesg         = "dmesg"
)

// Event is one sign that the USB tree is struggling, attributed to a port.
// The poller leaves Time zero; the controller stamps it from its clock.
type Event struct {
	Port   string    `json:"port"`
	Kind   string    `json:"kind"`
	Detail string    `json:"detail"`
	Time   time.Time `json:"time"`
}

// Dmesg says whether the kernel log could be read. dmesg_restrict=1 makes it
// unreadable in the pod today, which is reported, not fatal.
type Dmesg struct {
	Available bool   `json:"available"`
	Detail    string `json:"detail"`
}
