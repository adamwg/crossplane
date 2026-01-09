import dagger
from typing import Annotated
from dagger import DefaultPath, Doc, dag, function, object_type


@object_type
class Crossplane:
    @function
    async def test(
            self,
            source: Annotated[
                dagger.Directory, DefaultPath("/"), Doc("crossplane source directory")
            ],
    ) -> str:
        return await (
            self.build_env(source)
            .with_exec(["go", "test", "-covermode=count", "-coverprofile=coverage.txt", "./apis/...", "./cmd/...", "./internal/..."])
            .stdout()
        )

    @function
    def build_env(
        self,
        source: Annotated[
            dagger.Directory, DefaultPath("/"), Doc("hello-dagger source directory")
        ],
    ) -> dagger.Container:
        """Build a ready-to-use development environment"""
        go_cache = dag.cache_volume("go")
        return (
            dag.container()
            .from_("golang:1.24.5")
            .with_directory("/", source)
            .with_mounted_cache("/root/.cache/go-build", go_cache)
            .with_workdir("/")
        )
