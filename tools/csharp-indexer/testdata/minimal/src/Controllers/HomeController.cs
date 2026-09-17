using Microsoft.AspNetCore.Mvc;

namespace Minimal.Web;

public class HomeController : Controller
{
    public IActionResult Index() => View();

    public IActionResult About() => View();
}
