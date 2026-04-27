# Install Notes

## Disk Layout

| Disk | By-ID | Size | Role |
|------|-------|------|------|
| nvme0n1 | `nvme-PCIe_SSD_912807390D2300009528` | 500GB | Root (FCOS/Magus) |
| nvme1n1 | `nvme-Samsung_SSD_990_PRO_4TB_S7KGNU0Y708625D` | 4TB | Data (`/var/data`) |

## Prerequisites

- BIOS: insecure PXE boot enabled (or USB boot)
- Ignition served over HTTP (e.g. `python3 -m http.server 9001`)
- Both NVMe disks wiped before first install: `wipefs -a /dev/disk/by-id/<disk>`

## Step 1: Install FCOS

Netboot.xyz -> Fedora CoreOS, or boot the FCOS bare metal ISO.

The ignition is minimal — just creates user `chuck` with SSH key and wheel group.
All other provisioning (groups, data disk, brew) is handled by Magus oneshot services.

```bash
sudo coreos-installer install \
  /dev/disk/by-id/nvme-PCIe_SSD_912807390D2300009528 \
  --ignition-url http://<your-ip>:9001/magus.ign
reboot
```

## Step 2: Switch to Magus

SSH in, then:

```bash
sudo bootc switch ghcr.io/lazypower/magus:latest
reboot
```

## What happens on first Magus boot

Three oneshot services run automatically (guarded by `/var/lib/magus/` stamps):

| Service | What it does |
|---------|-------------|
| `magus-provision-user` | Adds chuck to video/render groups, sets shell to zsh |
| `magus-provision-data` | Formats 4TB as XFS if needed, fstab entry, mounts /var/data, creates model dirs |
| `magus-provision-brew` | Installs Linuxbrew, runs Brewfile if present |

## Subsequent reimages

The data disk survives reimages. Only the root disk gets replaced.

```bash
sudo bootc upgrade   # pull latest image
# or
sudo bootc switch ghcr.io/lazypower/magus:latest  # switch to a different image
reboot
```

## Gotchas learned the hard way

- **Use by-id disk paths everywhere.** Device names (`nvme0n1`/`nvme1n1`) swap between boots.
- **FCOS ignition must be minimal.** Groups like `video`/`render` don't exist in vanilla FCOS. Shell `zsh` doesn't exist either. Keep ignition to user + SSH + wheel only.
- **Butane `with_mount_unit: true` races fsck.** Don't use it for secondary disks. Use fstab entries via oneshot services instead.
- **Netboot.xyz may lack NIC drivers** for Strix Halo. FCOS ISO is the reliable fallback.
