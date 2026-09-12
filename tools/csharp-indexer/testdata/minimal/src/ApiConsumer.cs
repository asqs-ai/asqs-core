using System.Net.Http;
using System.Net.Http.Json;
using System.Threading.Tasks;

namespace Minimal.Client;

public class ApiConsumer
{
    private readonly HttpClient http = new();

    public async Task<string?> Fetch(string id) => await http.GetFromJsonAsync<string>($"api/basketapi/{id}");

    public async Task Push() => await http.PostAsJsonAsync("api/basketapi", new { });
}
