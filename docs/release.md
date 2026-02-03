Release procedure
=================

This document describes how to release a new version.

## Labeling

Release notes are automatically generated based on PRs included in the release.
Those PRs are categorized based on the label assigned to them.
Please refer to `.github/release.yml` for the kind of labels.

## Versioning

Follow [semantic versioning 2.0.0][semver] to choose the new version number.

## Bump version

1. Determine a new version number and set the `VERSION` variable.

    ```console
    # Set VERSION and confirm it. It should not have a "v" prefix.
    $ VERSION=x.y.z
    $ echo $VERSION
    ```

2. Update the chart version and appVersion in `charts/ofen/Chart.yaml`.

    ```console
    $ sed -i -E "s/^version: .+/version: $VERSION/" charts/ofen/Chart.yaml
    $ sed -i -E "s/^appVersion: .+/appVersion: \"$VERSION\"/" charts/ofen/Chart.yaml
    ```

3. Create a PR to bump the version and merge it.

    ```console
    $ git checkout -b bump-version-to-$VERSION
    $ git add charts/ofen/Chart.yaml
    $ git commit -m "Bump version to $VERSION"
    $ git push
    ```

4. Add a git tag to the main branch HEAD and push it.

    ```console
    # Set VERSION again.
    $ VERSION=x.y.z
    $ echo $VERSION

    $ git checkout main
    $ git pull
    $ git tag -a -m "Release v$VERSION" "v$VERSION"

    # Make sure the release tag exists.
    $ git tag -ln | grep $VERSION

    $ git push origin "v$VERSION"
    ```

GitHub actions will build and push artifacts such as container images and
create a new GitHub release.

[semver]: https://semver.org/spec/v2.0.0.html
