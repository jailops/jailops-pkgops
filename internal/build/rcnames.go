package build

// baseRCNames are the names the rc.d scripts of the FreeBSD base system provide
// (the `# PROVIDE:` lines of /etc/rc.d on 15.2), including the pseudo targets
// such as DAEMON, LOGIN and NETWORKING. A REQUIRE or BEFORE of an installed
// rc.d script may name one of them without the package installing it.
var baseRCNames = func() map[string]bool {
	m := map[string]bool{}
	for _, n := range []string{
		"DAEMON", "FILESYSTEMS", "LOGIN", "NETWORK", "NETWORKING", "SERVERS",
		"accounting", "adjkerntz", "auditd", "auditdistd", "automount", "automountd", "autounmountd", "backlight",
		"bgfsck", "blacklistd", "blocklistd", "bluetooth", "bootparams", "bridge", "bsnmpd", "bthidd", "cfumass",
		"cleanvar", "cleartmp", "cron", "ctld", "ddb", "defaultroute", "devd", "devfs", "devmatch", "dhclient",
		"disks", "dmesg", "dnctl", "dumpon", "dynamicrouting", "fsck", "ftp-proxy", "geli2", "ggated", "gptboot",
		"growfs", "growfs_fstab", "growfs_postboot", "gssd", "hastd", "hcsecd", "hostapd", "hostid", "hostid_save",
		"hostname", "inetd", "iovctl", "ip6addrctl", "ipfilter", "ipfs", "ipfw", "ipfw_netflow", "ipmon", "ipnat",
		"ippool", "ipsec", "iscsictl", "iscsid", "jail", "kadmind", "kdc", "kld", "kldxref", "ldconfig", "linux",
		"local", "local_unbound", "localpkg", "lockd", "lpd", "mail", "mdconfig", "mdconfig2", "mixer", "motd",
		"mountcritlocal", "mountcritremote", "mountd", "mountlate", "moused", "msconvd", "msgs", "natd", "netif",
		"netoptions", "netwait", "newsyslog", "nfscbd", "nfsclient", "nfsd", "nfsuserd", "nisdomain", "noshutdown",
		"nscd", "ntpd", "ntpdate", "nuageinit", "nuageinit_post_net", "nuageinit_user_data_script", "os-release",
		"pf", "pflog", "pfsync", "power_profile", "powerd", "ppp", "pppoed", "pwcheck", "quota", "random", "rarpd",
		"rctl", "resolv", "rfcomm_pppd_server", "root", "route6d", "routed", "routing", "rpcbind", "rtadvd",
		"rtsold", "rwho", "savecore", "sdpd", "securelevel", "serial", "sshd", "statd", "static_arp", "static_ndp",
		"stf", "swap", "swaplate", "syscons", "sysctl", "sysctl_lastload", "syslogd", "sysvipc", "tlsclntd",
		"tlsservd", "tmp", "ubthidhci", "ugidfw", "utx", "var", "var_run", "virecover", "virtual_oss", "watchdogd",
		"wpa_supplicant", "ypbind", "ypldap", "yppasswdd", "ypserv", "ypset", "ypupdated", "ypxfrd", "zfs", "zfsbe",
		"zfsd", "zfskeys", "zpool", "zpoolreguid", "zpoolupgrade", "zvol",
	} {
		m[n] = true
	}
	return m
}()

// conflictingDeps are dependency origins that name one flavour of a package
// whose other flavours conflict with it: pkg dependencies are by name, so a
// host that has a different flavour installed is offered a replacement of the
// user's package. The reason is the text of the lint finding.
var conflictingDeps = map[string]string{
	"devel/git":      "git",
	"devel/git-lite": "git",
	"devel/git-tiny": "git",
}
