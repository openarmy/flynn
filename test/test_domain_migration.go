package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	c "github.com/flynn/flynn/Godeps/_workspace/src/github.com/flynn/go-check"
	"github.com/flynn/flynn/controller/client"
	ct "github.com/flynn/flynn/controller/types"
)

type DomainMigrationSuite struct {
	Helper
}

var _ = c.Suite(&DomainMigrationSuite{})

func (s *DomainMigrationSuite) migrateDomain(t *c.C, dm *ct.DomainMigration) {
	debugf(t, "migrating domain from %s to %s", dm.OldDomain, dm.Domain)
	client := s.controllerClient(t)

	events := make(chan *ct.Event)
	stream, err := client.StreamEvents(controller.StreamEventsOptions{
		ObjectTypes: []ct.EventType{ct.EventTypeDomainMigration},
	}, events)
	t.Assert(err, c.IsNil)
	defer stream.Close()

	prevRouterRelease, err := client.GetAppRelease("router")
	t.Assert(err, c.IsNil)

	err = client.PutDomain(dm)
	t.Assert(err, c.IsNil)

	waitEvent := func(typ string, timeout time.Duration) (event ct.DomainMigrationEvent) {
		debugf(t, "waiting for %s domain migration event", typ)
		var e *ct.Event
		var ok bool
		select {
		case e, ok = <-events:
			if !ok {
				t.Fatal("event stream closed unexpectedly")
			}
			debugf(t, "got %s domain migration event", typ)
		case <-time.After(timeout):
			t.Fatalf("timed out waiting for %s domain migration event", typ)
		}
		t.Assert(e.Data, c.NotNil)
		t.Assert(json.Unmarshal(e.Data, &event), c.IsNil)
		return
	}

	// created
	event := waitEvent("initial", 2*time.Minute)
	t.Assert(event.Error, c.Equals, "")
	t.Assert(event.DomainMigration, c.NotNil)
	t.Assert(event.DomainMigration.ID, c.Equals, dm.ID)
	t.Assert(event.DomainMigration.OldDomain, c.Equals, dm.OldDomain)
	t.Assert(event.DomainMigration.Domain, c.Equals, dm.Domain)
	t.Assert(event.DomainMigration.TLSCert, c.IsNil)
	t.Assert(event.DomainMigration.OldTLSCert, c.NotNil)
	t.Assert(event.DomainMigration.CreatedAt, c.NotNil)
	t.Assert(event.DomainMigration.CreatedAt.Equal(*dm.CreatedAt), c.Equals, true)
	t.Assert(event.DomainMigration.FinishedAt, c.IsNil)

	// complete
	event = waitEvent("final", 3*time.Minute)
	t.Assert(event.Error, c.Equals, "")
	t.Assert(event.DomainMigration, c.NotNil)
	t.Assert(event.DomainMigration.ID, c.Equals, dm.ID)
	t.Assert(event.DomainMigration.OldDomain, c.Equals, dm.OldDomain)
	t.Assert(event.DomainMigration.Domain, c.Equals, dm.Domain)
	t.Assert(event.DomainMigration.TLSCert, c.NotNil)
	t.Assert(event.DomainMigration.OldTLSCert, c.NotNil)
	t.Assert(event.DomainMigration.CreatedAt, c.NotNil)
	t.Assert(event.DomainMigration.CreatedAt.Equal(*dm.CreatedAt), c.Equals, true)
	t.Assert(event.DomainMigration.FinishedAt, c.NotNil)

	cert := event.DomainMigration.TLSCert

	controllerRelease, err := client.GetAppRelease("controller")
	t.Assert(err, c.IsNil)
	t.Assert(controllerRelease.Env["DEFAULT_ROUTE_DOMAIN"], c.Equals, dm.Domain)
	t.Assert(controllerRelease.Env["CA_CERT"], c.Equals, cert.CACert)

	routerRelease, err := client.GetAppRelease("router")
	t.Assert(err, c.IsNil)
	t.Assert(routerRelease.Env["TLSCERT"], c.Equals, cert.Cert)
	t.Assert(routerRelease.Env["TLSKEY"], c.Not(c.Equals), "")
	t.Assert(routerRelease.Env["TLSKEY"], c.Not(c.Equals), prevRouterRelease.Env["TLSKEY"])

	dashboardRelease, err := client.GetAppRelease("dashboard")
	t.Assert(err, c.IsNil)
	t.Assert(dashboardRelease.Env["DEFAULT_ROUTE_DOMAIN"], c.Equals, dm.Domain)
	t.Assert(dashboardRelease.Env["CONTROLLER_DOMAIN"], c.Equals, fmt.Sprintf("controller.%s", dm.Domain))
	t.Assert(dashboardRelease.Env["URL"], c.Equals, fmt.Sprintf("https://dashboard.%s", dm.Domain))
	t.Assert(dashboardRelease.Env["CA_CERT"], c.Equals, cert.CACert)

	var doPing func(string, int)
	doPing = func(component string, retriesRemaining int) {
		url := fmt.Sprintf("http://%s.%s/ping", component, dm.Domain)
		res, err := (&http.Client{}).Get(url)
		if (err != nil || res.StatusCode != 200) && retriesRemaining > 0 {
			time.Sleep(100 * time.Millisecond)
			doPing(component, retriesRemaining-1)
			return
		}
		t.Assert(err, c.IsNil)
		t.Assert(res.StatusCode, c.Equals, 200, c.Commentf("failed to ping %s", component))
	}
	doPing("controller", 3)
	doPing("dashboard", 3)
}

func (s *DomainMigrationSuite) TestDomainMigration(t *c.C) {
	release, err := s.controllerClient(t).GetAppRelease("controller")
	t.Assert(err, c.IsNil)
	oldDomain := release.Env["DEFAULT_ROUTE_DOMAIN"]

	// using xip.io to get around modifying /etc/hosts
	dm := &ct.DomainMigration{
		OldDomain: oldDomain,
		Domain:    fmt.Sprintf("%s.xip.io", routerIP),
	}
	s.migrateDomain(t, dm)
	s.migrateDomain(t, &ct.DomainMigration{
		OldDomain: dm.Domain,
		Domain:    dm.OldDomain,
	})
}
