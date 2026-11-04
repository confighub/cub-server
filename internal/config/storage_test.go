package config

import (
	"strings"
	"testing"
)

func storageTestOptions(class, size string) Options {
	o := ociTestOptions("")
	o.StorageClass, o.StorageSize = class, size
	return o
}

// Named, the class is on the database's claim; the cluster's default class is
// not consulted.
func TestStorageClassOnTheDatabaseClaim(t *testing.T) {
	db := renderFiles(t, storageTestOptions("auto-gp3", "20Gi"))[ConfigDir+"/30-database.yaml"]
	if !strings.Contains(db, "storageClassName: auto-gp3") {
		t.Errorf("the claim does not name the StorageClass:\n%s", db)
	}
	if !strings.Contains(db, "storage: 20Gi") {
		t.Errorf("the claim does not ask for the size given:\n%s", db)
	}
}

// Unnamed, storageClassName is absent rather than empty: empty asks for no
// dynamic provisioning, absent for the cluster's default class.
func TestNoStorageClassLeavesTheDefault(t *testing.T) {
	db := renderFiles(t, storageTestOptions("", ""))[ConfigDir+"/30-database.yaml"]
	if strings.Contains(db, "storageClassName:") {
		t.Errorf("the claim names a StorageClass with none given:\n%s", db)
	}
	// A render applied by hand says, beside the claim, what it needs of the
	// cluster.
	if !strings.Contains(db, "On a cluster with no default") || !strings.Contains(db, "--storage-class") {
		t.Errorf("the claim carries no word on the StorageClass it needs:\n%s", db)
	}
	if !strings.Contains(db, "storage: "+DefaultStorageSize) {
		t.Errorf("the claim does not ask for %s:\n%s", DefaultStorageSize, db)
	}
}

func TestValidateStorage(t *testing.T) {
	for _, ok := range []struct{ class, size string }{
		{"", ""},
		{"auto-gp3", "20Gi"},
		{"gp3.ebs", "500Mi"},
		{"standard", "1.5Ti"},
		{"standard", "100G"},
	} {
		opts := storageTestOptions(ok.class, ok.size)
		opts.Defaults()
		if err := opts.Validate(); err != nil {
			t.Errorf("%+v: unexpected error %v", ok, err)
		}
	}
	for _, bad := range []struct{ class, size, flag string }{
		{"Auto_GP3", "", "--storage-class"},
		{"-gp3", "", "--storage-class"},
		{"", "20 GB", "--storage-size"},
		{"", "0Gi", "--storage-size"},
		{"", "-1Gi", "--storage-size"},
		{"", "Gi", "--storage-size"},
	} {
		opts := storageTestOptions(bad.class, bad.size)
		opts.Defaults()
		err := opts.Validate()
		if err == nil || !strings.Contains(err.Error(), bad.flag) {
			t.Errorf("%+v: want an error naming %s, got %v", bad, bad.flag, err)
		}
	}
}

// An external database has no volume here to put on a class.
func TestStorageClassWithAnExternalDatabase(t *testing.T) {
	opts := storageTestOptions("auto-gp3", "")
	opts.Database, opts.DatabaseURL = DatabaseExternal, "postgresql://db.example.com/confighub"
	opts.Defaults()
	if err := opts.Validate(); err == nil || !strings.Contains(err.Error(), "--storage-class") {
		t.Errorf("want an error naming --storage-class, got %v", err)
	}
}
