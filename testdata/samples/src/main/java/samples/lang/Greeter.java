package samples.lang;

public interface Greeter {
    String greet(String who);

    default String greetLoud(String who) {
        return greet(who).toUpperCase();
    }
}
