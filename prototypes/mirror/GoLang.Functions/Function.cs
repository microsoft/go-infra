using System;
using GoLang.Mirror;
using static GoLang.Mirror.Constants;
using Microsoft.Azure.WebJobs;
using Microsoft.Azure.WebJobs.Host;
using Microsoft.Extensions.Logging;
using Microsoft.AspNetCore.Mvc;
using Microsoft.Azure.WebJobs.Extensions.Http;
using Microsoft.AspNetCore.Http;
using System.Text;

namespace GoLang.Functions
{
    public class Function
    {
        public PackageMirrorUtil PackageMirrorUtil { get; }

        public Function(PackageMirrorUtil packageMirrorUtil)
        {
            PackageMirrorUtil = packageMirrorUtil;
        }

        [FunctionName("status")]
        public async Task<IActionResult> OnStatusAsync(
            [HttpTrigger(AuthorizationLevel.Anonymous, "get", "post", Route = null)] HttpRequest req,
            ILogger logger)
        {
            var builder = new StringBuilder();
            var delta = DateTimeOffset.UtcNow - TimeSpan.FromHours(1);
            foreach (var version in await PackageMirrorUtil.GetUnmirroredIndexPackageVersionsAsync(delta))
            {
                builder.Append(version.ToString());
            }

            return new ContentResult()
            {
                Content = builder.ToString(),
                ContentType = "text/html",
            };
        }

        [FunctionName("mirror-package-version")]
        public async Task MirrorPackageVersion([QueueTrigger(QueueNameMirrorPackageVersion, Connection = SecretNameStorageConnectionString)]string item, ILogger log)
        {
            var parts = item.Split('#');
            await PackageMirrorUtil.MirrorPackageVersionAsync(parts[0], parts[1], overwrite: false);
        }

        [FunctionName("mirror-package-index")]
        public async Task MirrorPackageIndex(
            [TimerTrigger("0 */2 * * * *")]TimerInfo timerInfo,
            [Queue(QueueNameMirrorPackageVersion, Connection = SecretNameStorageConnectionString)] IAsyncCollector<string> mirrorCollector,
            ILogger log)
        {
            var delta = DateTimeOffset.UtcNow - TimeSpan.FromHours(1);
            foreach (var version in await PackageMirrorUtil.GetUnmirroredIndexPackageVersionsAsync(delta))
            {
                await mirrorCollector.AddAsync($"{version.PackageName}#{version.Version}");
            }
        }
    }
}
