package samples.lang;

public class FriendlyGreeter implements Greeter {
    @Override
    public String greet(String who) {
        return "hello " + who;
    }
}
