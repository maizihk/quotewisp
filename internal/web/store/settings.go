package store

import (
	"context"
	"database/sql"
	"errors"
)

type SiteSettings struct {
	Name, EnglishName, Slogan, Contact, PublicOrigin, RepoURL, BeianText, BeianURL string
}

func (s *Store) GetSettings(ctx context.Context) (SiteSettings, error) {
	return scanSettings(s.DB.QueryRowContext(ctx, `SELECT site_name, english_name, slogan, contact, public_origin, repo_url, beian_text, beian_url FROM site_settings WHERE id = 1`))
}

func (s *Store) EnsureSettings(ctx context.Context, seed SiteSettings) (SiteSettings, error) {
	seed = NormalizeSiteSettings(seed)
	if err := ValidateSiteSettings(seed); err != nil {
		return SiteSettings{}, err
	}
	q := `INSERT OR IGNORE INTO site_settings (id, site_name, english_name, slogan, contact, public_origin, repo_url, beian_text, beian_url) VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?)`

	_, err := s.DB.ExecContext(ctx, q,
		seed.Name, nullString(seed.EnglishName), nullString(seed.Slogan), seed.Contact, nullString(seed.PublicOrigin), nullString(seed.RepoURL), nullString(seed.BeianText), nullString(seed.BeianURL))
	if err != nil {
		return SiteSettings{}, errors.New("write site settings")
	}
	got, err := s.GetSettings(ctx)
	if err != nil {
		return SiteSettings{}, errors.New("read site settings")
	}
	return got, nil
}

func (s *Store) UpsertSettings(ctx context.Context, in SiteSettings) error {
	in = NormalizeSiteSettings(in)
	if err := ValidateSiteSettings(in); err != nil {
		return err
	}
	q := `INSERT INTO site_settings (id, site_name, english_name, slogan, contact, public_origin, repo_url, beian_text, beian_url) VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(id) DO UPDATE SET site_name=excluded.site_name, english_name=excluded.english_name, slogan=excluded.slogan, contact=excluded.contact, public_origin=excluded.public_origin, repo_url=excluded.repo_url, beian_text=excluded.beian_text, beian_url=excluded.beian_url`
	args := []any{in.Name, nullString(in.EnglishName), nullString(in.Slogan), in.Contact, nullString(in.PublicOrigin), nullString(in.RepoURL), nullString(in.BeianText), nullString(in.BeianURL)}

	_, err := s.DB.ExecContext(ctx, q,
		args...)
	if err != nil {
		return errors.New("write site settings")
	}
	return nil
}

func scanSettings(row *sql.Row) (SiteSettings, error) {
	var s SiteSettings
	var englishName, slogan, origin, repo, beianText, beianURL sql.NullString
	err := row.Scan(&s.Name, &englishName, &slogan, &s.Contact, &origin, &repo, &beianText, &beianURL)
	if errors.Is(err, sql.ErrNoRows) {
		return SiteSettings{}, ErrNotFound
	}
	if err != nil {
		return SiteSettings{}, errors.New("read site settings")
	}
	s.EnglishName = coalesceString(englishName)
	s.Slogan = coalesceString(slogan)
	s.PublicOrigin = coalesceString(origin)
	s.RepoURL = coalesceString(repo)
	s.BeianText = coalesceString(beianText)
	s.BeianURL = coalesceString(beianURL)
	return s, nil
}
