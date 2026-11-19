package schedule

import (
	"cmp"
	"slices"
	"time"

	bolt "go.etcd.io/bbolt"
)

func (s *Store) Templates() ([]Template, error) {
	var result []Template
	err := s.view(func(tx *bolt.Tx) error {
		return eachRecord(tx, tmplBucket, func(template Template) error {
			result = append(result, template)
			return nil
		})
	})
	slices.SortFunc(result, func(a, b Template) int {
		return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.ID, b.ID))
	})
	return result, err
}

func (s *Store) Template(id string) (Template, error) {
	return get[Template](s, tmplBucket, id)
}

func (s *Store) CreateTemplate(name, prompt string, now time.Time) (Template, error) {
	return s.putTemplate("", name, prompt, now)
}

func (s *Store) EditTemplate(id, name, prompt string, now time.Time) (Template, error) {
	if id == "" {
		return Template{}, ErrNotFound
	}
	return s.putTemplate(id, name, prompt, now)
}

func (s *Store) putTemplate(id, name, prompt string, now time.Time) (Template, error) {
	if err := validateNamePrompt(name, prompt); err != nil {
		return Template{}, err
	}
	now = now.UTC()
	var result Template
	err := s.update(func(tx *bolt.Tx) error {
		var template Template
		if id == "" {
			template = Template{ID: NewID(now), CreatedAt: now}
		} else {
			var err error
			template, err = record[Template](tx, tmplBucket, id)
			if err != nil {
				return err
			}
		}
		template.Name, template.Prompt, template.UpdatedAt = name, prompt, now
		result = template
		return putRecord(tx, tmplBucket, template.ID, template)
	})
	if err != nil {
		return Template{}, err
	}
	return result, nil
}

func (s *Store) DeleteTemplate(id string) error {
	return s.update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte(tmplBucket))
		if bucket.Get([]byte(id)) == nil {
			return ErrNotFound
		}
		return bucket.Delete([]byte(id))
	})
}
