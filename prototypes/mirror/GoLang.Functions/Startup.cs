using Azure.Storage.Blobs;
using GoLang.Mirror;
using Microsoft.Azure.Functions.Extensions.DependencyInjection;
using Microsoft.Extensions.Configuration;
using Microsoft.Extensions.Configuration.AzureKeyVault;
using Microsoft.Extensions.DependencyInjection;
using System;
using System.Collections.Generic;
using System.Linq;
using System.Text;
using System.Threading.Tasks;

[assembly: FunctionsStartup(typeof(GoLang.Functions.Startup))]

namespace GoLang.Functions
{
    internal class Startup : FunctionsStartup
    {
        public override void Configure(IFunctionsHostBuilder builder)
        {
            var config = new ConfigurationBuilder()
                .AddEnvironmentVariables()
                .Build();

            builder.Services.AddScoped<PackageMirrorUtil>(_ =>
            {
                return new PackageMirrorUtil(config[Constants.SecretNameStorageConnectionString]);
            });
        }
    }
}
