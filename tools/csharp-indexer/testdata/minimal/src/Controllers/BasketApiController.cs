using Microsoft.AspNetCore.Mvc;

namespace Minimal.Api;

[ApiController]
[Route("api/[controller]")]
public class BasketApiController : ControllerBase
{
    [HttpGet("{id}")]
    public IActionResult GetById(string id) => Ok(id);

    [HttpPost]
    public IActionResult Create() => Ok();

    [HttpHead("ping")]
    public IActionResult Ping() => Ok();

    // A [Route] with no verb attribute is a GET by ASP.NET's own convention.
    [Route("legacy/{id}")]
    public IActionResult Legacy(string id) => Ok(id);
}
