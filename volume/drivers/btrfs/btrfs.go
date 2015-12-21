// +build linux,have_btrfs

package btrfs

import (
	"fmt"
	"path"
	"syscall"
	"time"

	"github.com/Sirupsen/logrus"
	"github.com/docker/docker/daemon/graphdriver"
	"github.com/docker/docker/daemon/graphdriver/btrfs"
	"github.com/libopenstorage/openstorage/api"
	"github.com/libopenstorage/openstorage/pkg/chaos"
	"github.com/libopenstorage/openstorage/volume"
	"github.com/pborman/uuid"
	"github.com/portworx/kvdb"
)

const (
	Name      = "btrfs"
	Type      = api.DriverType_DRIVER_TYPE_FILE
	RootParam = "home"
	Volumes   = "volumes"
)

var (
	koStrayCreate chaos.ID
	koStrayDelete chaos.ID
)

type driver struct {
	*volume.IoNotSupported
	*volume.DefaultBlockDriver
	*volume.DefaultEnumerator
	btrfs graphdriver.Driver
	root  string
}

func Init(params volume.DriverParams) (volume.VolumeDriver, error) {
	root, ok := params[RootParam]
	if !ok {
		return nil, fmt.Errorf("Root directory should be specified with key %q", RootParam)
	}
	home := path.Join(root, Volumes)
	d, err := btrfs.Init(home, nil, nil, nil)
	if err != nil {
		return nil, err
	}
	s := volume.NewDefaultEnumerator(Name, kvdb.Instance())
	return &driver{
		btrfs:             d,
		root:              root,
		IoNotSupported:    &volume.IoNotSupported{},
		DefaultEnumerator: s}, nil
}

func (d *driver) String() string {
	return Name
}

// Status diagnostic information
func (d *driver) Status() [][2]string {
	return d.btrfs.Status()
}

func (d *driver) Type() api.DriverType {
	return Type
}

// Create a new subvolume. The volume spec is not taken into account.
func (d *driver) Create(
	locator *api.VolumeLocator,
	source *api.Source,
	spec *api.VolumeSpec,
) (string, error) {

	if spec.Format != api.FSType_FS_TYPE_BTRFS && spec.Format != api.FSType_FS_TYPE_NONE {
		return "", fmt.Errorf("Filesystem format (%v) must be %v", spec.Format.SimpleString(), api.FSType_FS_TYPE_BTRFS.SimpleString())
	}

	volumeID := uuid.New()

	v := &api.Volume{
		ID:       volumeID,
		Locator:  locator,
		Ctime:    time.Now(),
		Spec:     spec,
		Source:   source,
		LastScan: time.Now(),
		Format:   "btrfs",
		State:    api.VolumeAvailable,
		Status:   api.Up,
	}
	err := d.CreateVol(v)
	if err != nil {
		return "", err
	}
	err = d.btrfs.Create(volumeID, "", "")
	if err != nil {
		return "", err
	}
	v.DevicePath, err = d.btrfs.Get(volumeID, "")
	if err != nil {
		return v.ID, err
	}
	err = d.UpdateVol(v)
	return v.ID, err
}

// Delete subvolume
func (d *driver) Delete(volumeID string) error {
	err := d.DeleteVol(volumeID)
	if err != nil {
		logrus.Println(err)
		return err
	}

	chaos.Now(koStrayDelete)
	if err == nil {
		err = d.btrfs.Remove(volumeID)
	}
	return err
}

// Mount bind mount btrfs subvolume
func (d *driver) Mount(volumeID string, mountpath string) error {
	v, err := d.GetVol(volumeID)
	if err != nil {
		logrus.Println(err)
		return err
	}
	err = syscall.Mount(v.DevicePath, mountpath, v.Format.SimpleString(), syscall.MS_BIND, "")
	if err != nil {
		return fmt.Errorf("Failed to mount %v at %v: %v", v.DevicePath, mountpath, err)
	}

	v.AttachPath = mountpath
	err = d.UpdateVol(v)

	return err
}

// Unmount btrfs subvolume
func (d *driver) Unmount(volumeID string, mountpath string) error {
	v, err := d.GetVol(volumeID)
	if err != nil {
		return err
	}
	if v.AttachPath == "" {
		return fmt.Errorf("Device %v not mounted", volumeID)
	}
	err = syscall.Unmount(v.AttachPath, 0)
	if err != nil {
		return err
	}
	v.AttachPath = ""
	err = d.UpdateVol(v)
	return err
}

func (d *driver) Set(volumeID string, locator *api.VolumeLocator, spec *api.VolumeSpec) error {
	if spec != nil {
		return volume.ErrNotSupported
	}
	v, err := d.GetVol(volumeID)
	if err != nil {
		return err
	}
	if locator != nil {
		v.Locator = *locator
	}
	err = d.UpdateVol(v)
	return err
}

// Snapshot create new subvolume from volume
func (d *driver) Snapshot(volumeID string, readonly bool, locator api.VolumeLocator) (string, error) {
	vols, err := d.Inspect([]string{volumeID})
	if err != nil {
		return "", err
	}
	if len(vols) != 1 {
		return "", fmt.Errorf("Failed to inspect %v len %v", volumeID, len(vols))
	}
	snapID := uuid.New()
	vols[0].ID = snapID
	vols[0].Source = &api.Source{Parent: volumeID}
	vols[0].Locator = locator
	vols[0].Ctime = time.Now()

	err = d.CreateVol(&vols[0])
	if err != nil {
		return "", err
	}
	chaos.Now(koStrayCreate)
	err = d.btrfs.Create(snapID, volumeID, "")
	if err != nil {
		return "", err
	}
	return vols[0].ID, nil
}

// Stats for specified volume.
func (d *driver) Stats(volumeID string) (api.Stats, error) {
	return api.Stats{}, nil
}

// Alerts on this volume.
func (d *driver) Alerts(volumeID string) (api.Alerts, error) {
	return api.Alerts{}, nil
}

// Shutdown and cleanup.
func (d *driver) Shutdown() {
}

func init() {
	volume.Register(Name, Init)
}
