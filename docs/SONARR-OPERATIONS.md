# Sonarr and Radarr operations

## Ownership boundary

Sonarr and Radarr should own media naming and rename operations.

Recyclarr should manage quality profiles and custom formats, but it should not manage media naming. The Recyclarr `media_naming` blocks were removed after they reverted Sonarr/Radarr naming while the apps were running.

## Sonarr naming

Desired Sonarr naming values:

```text
Standard Episode Format:
{Series CleanTitleYear} - S{season:00}E{episode:00} - {Episode CleanTitle:90} {[Custom Formats]}{[Quality Full]}{[Mediainfo AudioCodec}{ Mediainfo AudioChannels]}{[MediaInfo VideoDynamicRangeType]}{[Mediainfo VideoCodec]}

Series Folder Format:
{Series CleanTitleYear} {tvdb-{TvdbId}}
```

The lowercase `tvdb` prefix is intentional. Plex supports external IDs in curly braces using source prefixes such as `{tvdb-123456}`.

Validate current Sonarr naming:

```sh
curl -fsS \
  -H "X-Api-Key: $SONARR_API_KEY" \
  "https://sonarr.cooney.site/api/v3/config/naming" |
  jq '{standardEpisodeFormat, seriesFolderFormat}'
```

Update Sonarr naming:

```sh
curl -fsS \
  -H "X-Api-Key: $SONARR_API_KEY" \
  "https://sonarr.cooney.site/api/v3/config/naming" \
  > /tmp/sonarr-naming.json

jq '
  .standardEpisodeFormat = "{Series CleanTitleYear} - S{season:00}E{episode:00} - {Episode CleanTitle:90} {[Custom Formats]}{[Quality Full]}{[Mediainfo AudioCodec}{ Mediainfo AudioChannels]}{[MediaInfo VideoDynamicRangeType]}{[Mediainfo VideoCodec]}" |
  .seriesFolderFormat = "{Series CleanTitleYear} {tvdb-{TvdbId}}"
' /tmp/sonarr-naming.json > /tmp/sonarr-naming.updated.json

curl -fsS \
  -X PUT \
  -H "X-Api-Key: $SONARR_API_KEY" \
  -H "Content-Type: application/json" \
  --data-binary @/tmp/sonarr-naming.updated.json \
  "https://sonarr.cooney.site/api/v3/config/naming" |
  jq '{standardEpisodeFormat, seriesFolderFormat}'
```

## Radarr naming

Desired Radarr naming values while normalizing movie folders:

```text
Movie file format:
{Movie CleanTitle} {(Release Year)} {tmdb-{TmdbId}} - {edition-{Edition Tags}} {[MediaInfo 3D]}{[Custom Formats]}{[Quality Full]}{[Mediainfo AudioCodec}{ Mediainfo AudioChannels]}{[MediaInfo VideoDynamicRangeType]}{[Mediainfo VideoCodec]}

Movie folder format:
{Movie CleanTitle} ({Release Year}) {tmdb-{TmdbId}}
```

Release group is intentionally omitted while normalizing names.

Validate current Radarr naming:

```sh
curl -fsS \
  -H "X-Api-Key: $RADARR_API_KEY" \
  "https://radarr.cooney.site/api/v3/config/naming" |
  jq '{renameMovies, standardMovieFormat, movieFolderFormat}'
```

## Recyclarr media naming override

Recyclarr previously managed Sonarr and Radarr `media_naming` settings. That caused app naming values to revert during a scheduled Recyclarr run.

Remove `media_naming` from both Sonarr and Radarr sections in:

```text
kubernetes/apps/default/recyclarr/app/resources/recyclarr.yml
```

Validate that Recyclarr no longer owns media naming:

```sh
yq '.sonarr.series.media_naming, .radarr.movies.media_naming' \
  kubernetes/apps/default/recyclarr/app/resources/recyclarr.yml
```

Expected:

```text
null
null
```

## Rename workflow

Recommended flow for large TV renames:

```text
1. Confirm Sonarr naming through the API.
2. Confirm Recyclarr media_naming is removed.
3. Rename in controlled batches from Sonarr.
4. Let Plex scan and reconcile before starting another large metadata action.
5. Watch Plex logs for `Part rename detected`.
6. Verify Plex show counts and duplicates after the scan settles.
```

Plex may temporarily show duplicates during a large rename batch. If logs show `Part rename detected`, Plex is usually reconciling the new path back to the existing metadata lineage and preserving watched state.

## Sidecar and symlink notes

Sonarr may not repair old sidecar symlinks. One observed pattern was a sidecar symlink that pointed to an old `Season.03` path after folders were normalized to `season03`. Plex then logged missing `.nfo` or `-thumb.jpg` files because the symlink target no longer existed.

Fix those manually or regenerate local metadata sidecars if Plex continues to log them.
