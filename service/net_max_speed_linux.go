//go:build linux

package service

import (
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	ethtoolCmdGset         = 0x00000001
	ethtoolCmdGlinksettings = 0x0000004c
)

// ifreqEthtool mirrors the kernel ifreq layout for use with SIOCETHTOOL.
// The padding [20]byte is intentionally oversized: on 64-bit the kernel reads
// 40 bytes (name=16 + union=24) and on 32-bit it reads 32 bytes (name=16 +
// union=16), so our struct is always large enough regardless of arch.
type ifreqEthtool struct {
	name [unix.IFNAMSIZ]byte
	data unsafe.Pointer
	_    [20]byte
}

// ethtoolGsetCmd mirrors the kernel struct ethtool_cmd (for ETHTOOL_GSET).
type ethtoolGsetCmd struct {
	Cmd          uint32
	Supported    uint32
	Advertising  uint32
	Speed        uint16
	Duplex       uint8
	Port         uint8
	PhyAddress   uint8
	Transceiver  uint8
	Autoneg      uint8
	MdioSupport  uint8
	Maxtxpkt     uint32
	Maxrxpkt     uint32
	SpeedHi      uint16
	EthTpMdix    uint8
	EthTpMdixCtrl uint8
	LpAdvertising int32
	Reserved     [2]uint32
}

// ethtoolLinkSettings mirrors the kernel struct ethtool_link_settings with
// room for up to 4 nwords of link_mode_masks (supported/advertising/lp).
type ethtoolLinkSettings struct {
	Cmd                 uint32
	Speed               uint32
	Duplex              uint8
	Port                uint8
	PhyAddress          uint8
	Autoneg             uint8
	MdioSupport         uint8
	EthTpMdix           uint8
	EthTpMdixCtrl       uint8
	LinkModeMasksNwords int8 // signed; kernel returns -(required nwords) on probe
	Transceiver         uint8
	MasterSlaveCfg      uint8
	MasterSlaveState    uint8
	RateMatching        uint8
	Reserved            [7]uint32
	// 3 groups (supported, advertising, lp_advertising) × up to 4 nwords each
	LinkModeMasks [12]uint32
}

// linkModeBitSpeed maps ETHTOOL_LINK_MODE_*_BIT positions to speeds in Mbps.
// Bits that represent capabilities other than speed (Autoneg, Pause, …) are omitted.
var linkModeBitSpeed = map[uint]int{
	0:  10,     // 10baseT_Half
	1:  10,     // 10baseT_Full
	2:  100,    // 100baseT_Half
	3:  100,    // 100baseT_Full
	4:  1000,   // 1000baseT_Half
	5:  1000,   // 1000baseT_Full
	12: 10000,  // 10000baseT_Full
	15: 2500,   // 2500baseX_Full
	17: 1000,   // 1000baseKX_Full
	18: 10000,  // 10000baseKX4_Full
	19: 10000,  // 10000baseKR_Full
	20: 10000,  // 10000baseR_FEC
	21: 20000,  // 20000baseMLD2_Full
	22: 20000,  // 20000baseKR2_Full
	23: 40000,  // 40000baseKR4_Full
	24: 40000,  // 40000baseCR4_Full
	25: 40000,  // 40000baseSR4_Full
	26: 40000,  // 40000baseLR4_Full
	27: 56000,  // 56000baseKR4_Full
	28: 56000,  // 56000baseCR4_Full
	29: 56000,  // 56000baseSR4_Full
	30: 56000,  // 56000baseLR4_Full
	31: 25000,  // 25000baseCR_Full
	32: 25000,  // 25000baseKR_Full
	33: 25000,  // 25000baseSR_Full
	34: 50000,  // 50000baseCR2_Full
	35: 50000,  // 50000baseKR2_Full
	36: 100000, // 100000baseKR4_Full
	37: 100000, // 100000baseSR4_Full
	38: 100000, // 100000baseCR4_Full
	39: 100000, // 100000baseLR4_ER4_Full
	40: 50000,  // 50000baseSR2_Full
	41: 1000,   // 1000baseX_Full
	42: 10000,  // 10000baseCR_Full
	43: 10000,  // 10000baseSR_Full
	44: 10000,  // 10000baseLR_Full
	45: 10000,  // 10000baseLRM_Full
	46: 10000,  // 10000baseER_Full
	47: 2500,   // 2500baseT_Full
	48: 5000,   // 5000baseT_Full
	52: 50000,  // 50000baseKR_Full
	53: 50000,  // 50000baseSR_Full
	54: 50000,  // 50000baseCR_Full
	55: 50000,  // 50000baseLR_ER_FR_Full
	56: 50000,  // 50000baseDR_Full
	57: 100000, // 100000baseKR2_Full
	58: 100000, // 100000baseSR2_Full
	59: 100000, // 100000baseCR2_Full
	60: 100000, // 100000baseLR2_ER2_FR2_Full
	61: 100000, // 100000baseDR2_Full
	62: 200000, // 200000baseKR4_Full
	63: 200000, // 200000baseSR4_Full
}

// gsetSupportedBitSpeed maps ethtool_cmd.Supported bitmask positions to Mbps.
var gsetSupportedBitSpeed = []struct {
	bit   uint
	speed int
}{
	{0, 10}, {1, 10},
	{2, 100}, {3, 100},
	{4, 1000}, {5, 1000},
	{12, 10000},
	{15, 2500},
	{17, 1000},
	{18, 10000}, {19, 10000}, {20, 10000},
	{21, 20000}, {22, 20000},
	{23, 40000}, {24, 40000}, {25, 40000}, {26, 40000},
	{27, 56000}, {28, 56000}, {29, 56000}, {30, 56000},
}

func doSIOCETHTOOL(fd int, ifname string, cmd unsafe.Pointer) error {
	var ifr ifreqEthtool
	copy(ifr.name[:], ifname)
	ifr.data = cmd
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), unix.SIOCETHTOOL, uintptr(unsafe.Pointer(&ifr)))
	if errno != 0 {
		return errno
	}
	return nil
}

func maxSpeedFromLinkModeMasks(masks []uint32) int {
	maxSpeed := 0
	for i, word := range masks {
		for bit := uint(0); bit < 32; bit++ {
			if word&(1<<bit) != 0 {
				globalBit := uint(i)*32 + bit
				if s, ok := linkModeBitSpeed[globalBit]; ok && s > maxSpeed {
					maxSpeed = s
				}
			}
		}
	}
	return maxSpeed
}

func getMaxSpeedViaLinkSettings(fd int, ifname string) (int, error) {
	var ls ethtoolLinkSettings
	ls.Cmd = ethtoolCmdGlinksettings

	// Step 1: probe for required nwords (kernel returns negative value)
	if err := doSIOCETHTOOL(fd, ifname, unsafe.Pointer(&ls)); err != nil {
		return 0, err
	}
	nwords := int8(-ls.LinkModeMasksNwords)
	if nwords <= 0 || int(nwords) > 4 {
		return 0, unix.ENOTSUP
	}

	// Step 2: request with correct nwords
	ls.LinkModeMasksNwords = nwords
	if err := doSIOCETHTOOL(fd, ifname, unsafe.Pointer(&ls)); err != nil {
		return 0, err
	}

	// Supported modes are the first nwords uint32s
	return maxSpeedFromLinkModeMasks(ls.LinkModeMasks[:nwords]), nil
}

func getMaxSpeedViaGset(fd int, ifname string) (int, error) {
	cmd := ethtoolGsetCmd{Cmd: ethtoolCmdGset}
	if err := doSIOCETHTOOL(fd, ifname, unsafe.Pointer(&cmd)); err != nil {
		return 0, err
	}
	maxSpeed := 0
	for _, entry := range gsetSupportedBitSpeed {
		if cmd.Supported&(1<<entry.bit) != 0 && entry.speed > maxSpeed {
			maxSpeed = entry.speed
		}
	}
	return maxSpeed, nil
}

func getNetMaxSpeedViaIoctl(ifname string) (int, error) {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM, unix.IPPROTO_IP)
	if err != nil {
		return 0, err
	}
	defer unix.Close(fd)

	if speed, err := getMaxSpeedViaLinkSettings(fd, ifname); err == nil && speed > 0 {
		return speed, nil
	}
	return getMaxSpeedViaGset(fd, ifname)
}
