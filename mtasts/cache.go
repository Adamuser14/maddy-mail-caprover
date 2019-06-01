package mtasts

import (
	"encoding/json"
	"errors"
	"io/ioutil"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/emersion/maddy/log"
)

func downloadPolicy(domain string) (*Policy, error) {
	// TODO: Consult OCSP/CRL to detect revoked certificates?

	resp, err := http.Get("https://mta-sts." + domain + "/.well-known/mta-sts.txt")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Policies fetched via HTTPS are only valid if the HTTP response code is
	// 200 (OK).  HTTP 3xx redirects MUST NOT be followed.
	if resp.StatusCode != 200 {
		return nil, errors.New("mtasts: HTTP " + resp.Status)
	}

	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/plain") {
		return nil, errors.New("mtasts: unexpected content type")
	}

	return readPolicy(resp.Body)
}

// Cache structure implements transparent MTA-STS policy caching using FS
// directory.
type Cache struct {
	Location string
	Logger   *log.Logger
}

var ErrNoPolicy = errors.New("mtasts: no MTA-STS policy found")

// Get reads policy from cache or tries to fetch it from Policy Host.
func (c *Cache) Get(domain string) (*Policy, error) {
	_, p, err := c.fetch(domain)
	return p, err
}

func (c *Cache) store(domain, id string, fetchTime time.Time, p *Policy) error {
	f, err := os.Create(filepath.Join(c.Location, domain))
	if err != nil {
		return err
	}
	defer f.Close()

	return json.NewEncoder(f).Encode(map[string]interface{}{
		"ID":        id,
		"FetchTime": fetchTime,
		"Policy":    p,
	})
}

func (c *Cache) load(domain string) (id string, fetchTime time.Time, p *Policy, err error) {
	f, err := os.Open(filepath.Join(c.Location, domain))
	if err != nil {
		return "", time.Time{}, nil, err
	}
	defer f.Close()

	data := struct {
		ID        string
		FetchTime time.Time
		Policy    *Policy
	}{}
	if err := json.NewDecoder(f).Decode(&data); err != nil {
		return "", time.Time{}, nil, err
	}
	return data.ID, data.FetchTime, data.Policy, nil
}

func (c *Cache) RefreshCache() error {
	dir, err := ioutil.ReadDir(c.Location)
	if err != nil {
		return err
	}

	for _, ent := range dir {
		if ent.IsDir() {
			continue
		}
		cacheHit, _, err := c.fetch(ent.Name())
		if err != nil {
			c.Logger.Printf("failed to update MTA-STS policy for %v: %v", ent.Name(), err)
		}
		if !cacheHit && err == nil {
			c.Logger.Debugln("updated MTA-STS policy for", ent.Name())
		}

		// This means cached version is expired and remote offers no updated policy.
		// Remove cached version to save space.
		if !cacheHit && err == ErrNoPolicy {
			if err := os.Remove(filepath.Join(c.Location, ent.Name())); err != nil {
				c.Logger.Println("failed to remove MTA-STS policy for", ent.Name())
			}
			c.Logger.Debugln("removed MTA-STS policy for", ent.Name())
		}
	}

	return nil
}

func (c *Cache) fetch(domain string) (cacheHit bool, p *Policy, err error) {
	validCache := true
	cachedId, fetchTime, cachedPolicy, err := c.load(domain)
	if err != nil {
		if !os.IsNotExist(err) {
			// Something wrong with FS directory used for caching, this is bad.
			return false, nil, err
		}

		validCache = false
	} else if fetchTime.Add(time.Duration(cachedPolicy.MaxAge) * time.Second).Before(time.Now()) {
		validCache = false
	}

	records, err := net.LookupTXT("_mta-sts." + domain)
	if err != nil {
		if derr, ok := err.(*net.DNSError); ok && !derr.IsTemporary {
			return false, nil, ErrNoPolicy
		}
		return false, nil, err
	}

	// RFC says:
	//   If the number of resulting records is not one, or if the resulting
	//   record is syntactically invalid, senders MUST assume the recipient
	//   domain does not have an available MTA-STS Policy. ...
	//   (Note that the absence of a usable TXT record is not by itself
	//   sufficient to remove a sender's previously cached policy for the Policy
	//   Domain, as discussed in Section 5.1, "Policy Application Control Flow".)
	if len(records) != 1 {
		if validCache {
			return true, cachedPolicy, nil
		}
		return false, nil, ErrNoPolicy
	}
	dnsId, err := readDNSRecord(records[0])
	if err != nil {
		if validCache {
			return true, cachedPolicy, nil
		}
		return false, nil, ErrNoPolicy
	}

	if !validCache || dnsId != cachedId {
		policy, err := downloadPolicy(domain)
		if err != nil {
			return false, nil, err
		}

		if err := c.store(domain, dnsId, time.Now(), policy); err != nil {
			// We still got up-to-date policy, cache is not critcial.
			return false, cachedPolicy, nil
		}
		return false, policy, nil
	}

	return true, cachedPolicy, nil
}
